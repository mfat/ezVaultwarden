package fetch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func layer(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// A registry serving one amd64 image made of the given layers, oldest first.
func fakeRegistry(t *testing.T, layers ...[]byte) (Image, *httptest.Server) {
	t.Helper()
	blobs := map[string][]byte{}
	var ls []map[string]string
	for _, l := range layers {
		blobs[digestOf(l)] = l
		ls = append(ls, map[string]string{"digest": digestOf(l)})
	}
	man, _ := json.Marshal(map[string]any{"layers": ls})
	idx, _ := json.Marshal(map[string]any{"manifests": []map[string]any{{
		"digest":   digestOf(man),
		"platform": map[string]string{"architecture": "amd64", "os": "linux"},
	}}})
	mans := map[string][]byte{digestOf(man): man, digestOf(idx): idx}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		digest := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		src := blobs
		if strings.Contains(r.URL.Path, "/manifests/") {
			src = mans
		}
		body, ok := src[digest]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return Image{Registry: srv.URL, Repo: "r/s", Digest: digestOf(idx)}, srv
}

func TestExtractTakesOnlyTheServerAndWebVault(t *testing.T) {
	img, _ := fakeRegistry(t,
		layer(t, map[string]string{"bin/sh": "shell", "etc/passwd": "root"}),
		layer(t, map[string]string{"vaultwarden": "server", "web-vault/index.html": "ui"}),
	)
	dest := t.TempDir()
	if err := ExtractVaultwarden(context.Background(), img, "amd64", dest, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dest, "vaultwarden"))
	if string(got) != "server" {
		t.Fatalf("binary = %q", got)
	}
	info, _ := os.Stat(filepath.Join(dest, "vaultwarden"))
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatal("binary is not executable")
	}
	if _, err := os.Stat(filepath.Join(dest, "web-vault", "index.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "etc", "passwd")); err == nil {
		t.Fatal("an unrelated file was extracted")
	}
}

func TestNewestLayerWins(t *testing.T) {
	img, _ := fakeRegistry(t,
		layer(t, map[string]string{"vaultwarden": "old", "web-vault/a": "old"}),
		layer(t, map[string]string{"vaultwarden": "new", "web-vault/a": "new"}),
	)
	dest := t.TempDir()
	if err := ExtractVaultwarden(context.Background(), img, "amd64", dest, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "vaultwarden")); string(got) != "new" {
		t.Fatalf("binary = %q", got)
	}
}

func TestTamperedIndexIsRefused(t *testing.T) {
	fastRetries(t)
	img, _ := fakeRegistry(t, layer(t, map[string]string{"vaultwarden": "x", "web-vault/a": "y"}))
	img.Digest = digestOf([]byte("something else"))
	err := ExtractVaultwarden(context.Background(), img, "amd64", t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected a failure")
	}
}

func TestTamperedLayerLeavesNothingBehind(t *testing.T) {
	fastRetries(t)
	good := layer(t, map[string]string{"vaultwarden": "server", "web-vault/a": "ui"})
	img, srv := fakeRegistry(t, good)
	real := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/blobs/") {
			w.Write(layer(t, map[string]string{"vaultwarden": "malicious", "web-vault/a": "ui"}))
			return
		}
		real.ServeHTTP(w, r)
	})
	dest := t.TempDir()
	err := ExtractVaultwarden(context.Background(), img, "amd64", dest, nil)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("wrote %v", entries)
	}
}

func TestMissingArchitectureIsExplained(t *testing.T) {
	img, _ := fakeRegistry(t, layer(t, map[string]string{"vaultwarden": "x", "web-vault/a": "y"}))
	err := ExtractVaultwarden(context.Background(), img, "riscv64", t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "riscv64") {
		t.Fatal(fmt.Sprint("got ", err))
	}
}
