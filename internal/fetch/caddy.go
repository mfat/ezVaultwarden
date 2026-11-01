package fetch

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
)

// ExtractCaddy downloads a release tarball, checks it against the pinned
// SHA-512 and writes only the caddy binary into dest.
func ExtractCaddy(ctx context.Context, url, sha512sum, dest string, report Report) error {
	open := func(ctx context.Context, offset int64) (*http.Response, error) {
		req, err := rangeRequest(ctx, url, offset)
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}
	f, err := download(ctx, open, func(r io.Reader) error {
		h := sha512.New()
		if _, err := io.Copy(h, r); err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != sha512sum {
			return fmt.Errorf("caddy checksum mismatch: got %s", got)
		}
		return nil
	}, report)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("caddy tarball holds no binary")
		}
		if err != nil {
			return err
		}
		if hdr.Name == "caddy" && hdr.Typeflag == tar.TypeReg {
			return writeFile(filepath.Join(dest, "caddy"), tr, 0o755)
		}
	}
}
