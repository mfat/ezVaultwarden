package fetch

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"path/filepath"
)

// ExtractCaddy downloads a release tarball, checks it against the pinned
// SHA-512 and writes only the caddy binary into dest.
func ExtractCaddy(ctx context.Context, url, sha512sum, dest string, report Report) error {
	return extractBinary(ctx, url, "caddy", sha512.New(), sha512sum, dest, report)
}

// ExtractLego does the same for lego, whose releases publish SHA-256.
func ExtractLego(ctx context.Context, url, sha256sum, dest string, report Report) error {
	return extractBinary(ctx, url, "lego", sha256.New(), sha256sum, dest, report)
}

func extractBinary(ctx context.Context, url, name string, h hash.Hash, sum, dest string, report Report) error {
	open := func(ctx context.Context, offset int64) (*http.Response, error) {
		req, err := rangeRequest(ctx, url, offset)
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}
	f, err := download(ctx, open, func(r io.Reader) error {
		h.Reset()
		if _, err := io.Copy(h, r); err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != sum {
			return fmt.Errorf("%s checksum mismatch: got %s", name, got)
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
			return fmt.Errorf("%s tarball holds no binary", name)
		}
		if err != nil {
			return err
		}
		if hdr.Name == name && hdr.Typeflag == tar.TypeReg {
			return writeFile(filepath.Join(dest, name), tr, 0o755)
		}
	}
}
