// Package fetch downloads the pinned server components and verifies them
// before anything is written where it could run.
package fetch

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Image names a multi-architecture image by the digest of its index, so a
// moved tag can never change what gets installed.
type Image struct {
	Registry string // "https://registry-1.docker.io"
	AuthURL  string // token endpoint, empty for an open registry
	Repo     string // "vaultwarden/server"
	Digest   string // "sha256:..."
}

const (
	mediaIndex = "application/vnd.oci.image.index.v1+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json"
	mediaManifest = "application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.docker.distribution.manifest.v2+json"
)

type index struct {
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant"`
		} `json:"platform"`
	} `json:"manifests"`
}

type manifest struct {
	Layers []struct {
		Digest string `json:"digest"`
	} `json:"layers"`
}

// ExtractVaultwarden writes the server binary and the web vault from img into
// dest. Those are all a static install needs, and taking both from one image
// guarantees a pair the Vaultwarden project built together.
func ExtractVaultwarden(ctx context.Context, img Image, arch, dest string, report Report) error {
	c := &registry{img: img}
	if err := c.authenticate(ctx); err != nil {
		return fmt.Errorf("registry login: %w", err)
	}
	var idx index
	if err := c.getVerified(ctx, img.Digest, mediaIndex, &idx); err != nil {
		return fmt.Errorf("image index: %w", err)
	}
	digest := ""
	for _, m := range idx.Manifests {
		if m.Platform.OS == "linux" && m.Platform.Architecture == arch {
			digest = m.Digest
			break
		}
	}
	if digest == "" {
		return fmt.Errorf("no Vaultwarden image for linux/%s", arch)
	}
	var man manifest
	if err := c.getVerified(ctx, digest, mediaManifest, &man); err != nil {
		return fmt.Errorf("image manifest: %w", err)
	}

	// Newest layer first, so an earlier layer can never replace a later file.
	found := extraction{dest: dest, seen: map[string]bool{}}
	var done int64
	for i := len(man.Layers) - 1; i >= 0; i-- {
		if err := c.extractLayer(ctx, man.Layers[i].Digest, &found, offsetReport(report, &done)); err != nil {
			return fmt.Errorf("image layer: %w", err)
		}
		if found.binary && found.webVault {
			return nil
		}
	}
	return errors.New("image holds no Vaultwarden binary or web vault")
}

type registry struct {
	img   Image
	token string
}

func (c *registry) authenticate(ctx context.Context) error {
	if c.img.AuthURL == "" {
		return nil
	}
	u := c.img.AuthURL + "&scope=" + url.QueryEscape("repository:"+c.img.Repo+":pull")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token request: %s", resp.Status)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	c.token = body.Token
	return nil
}

// open returns the raw response for a blob, whose status download interprets.
func (c *registry) open(ctx context.Context, kind, digest, accept string, offset int64) (*http.Response, error) {
	u := fmt.Sprintf("%s/v2/%s/%s/%s", c.img.Registry, c.img.Repo, kind, digest)
	req, err := rangeRequest(ctx, u, offset)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return http.DefaultClient.Do(req)
}

func (c *registry) getVerified(ctx context.Context, digest, accept string, into any) error {
	resp, err := c.open(ctx, "manifests", digest, accept, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", digest, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := checkDigest(digest, sha256.Sum256(body)); err != nil {
		return err
	}
	return json.Unmarshal(body, into)
}

func checkDigest(want string, sum [sha256.Size]byte) error {
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("digest mismatch: got %s, want %s", got, want)
	}
	return nil
}

func (c *registry) downloadBlob(ctx context.Context, digest string, report Report) (*os.File, error) {
	open := func(ctx context.Context, offset int64) (*http.Response, error) {
		return c.open(ctx, "blobs", digest, "", offset)
	}
	return download(ctx, open, func(r io.Reader) error {
		h := sha256.New()
		if _, err := io.Copy(h, r); err != nil {
			return err
		}
		var sum [sha256.Size]byte
		copy(sum[:], h.Sum(nil))
		return checkDigest(digest, sum)
	}, report)
}

type extraction struct {
	dest     string
	seen     map[string]bool
	binary   bool
	webVault bool
}

// offsetReport turns one layer's count into a total, and keeps the running sum
// in done so the next layer continues from it.
func offsetReport(report Report, done *int64) Report {
	base := *done
	return func(received int64) {
		*done = base + received
		if report != nil {
			report(*done)
		}
	}
}

func (c *registry) extractLayer(ctx context.Context, digest string, x *extraction, report Report) error {
	f, err := c.downloadBlob(ctx, digest, report)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	return x.read(gz)
}

// read keeps only the two things we want and ignores the rest of the image,
// so nothing else in a layer can land on disk.
func (x *extraction) read(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		wanted := name == "vaultwarden" || strings.HasPrefix(name, "web-vault/")
		if !wanted || hdr.Typeflag != tar.TypeReg || x.seen[name] {
			continue
		}
		if strings.Contains("/"+name+"/", "/../") || strings.HasPrefix(path.Base(name), ".wh.") {
			continue
		}
		x.seen[name] = true
		mode := os.FileMode(0o644)
		if name == "vaultwarden" {
			mode = 0o755
			x.binary = true
		} else {
			x.webVault = true
		}
		if err := writeFile(filepath.Join(x.dest, filepath.FromSlash(name)), tr, mode); err != nil {
			return err
		}
	}
}

func writeFile(dst string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
