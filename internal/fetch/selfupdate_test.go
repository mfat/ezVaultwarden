package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.10.0", "v0.9.0", true}, // numbers, not text
		{"v1.0.0", "v0.99.99", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v0.2.0", "dev", false},
		{"garbage", "v0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func release(t *testing.T, binary, sums string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "VERSION":
			fmt.Fprintln(w, "v0.2.0")
		case "checksums.txt":
			fmt.Fprint(w, sums)
		case "ezvaultwarden_linux_amd64":
			fmt.Fprint(w, binary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func sumOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func installed(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ezvaultwarden")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLatestVersionTrimsTheFile(t *testing.T) {
	got, err := LatestVersion(context.Background(), release(t, "", ""))
	if err != nil || got != "v0.2.0" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReplaceBinarySwapsAVerifiedBuild(t *testing.T) {
	base := release(t, "new", sumOf("new")+"  ezvaultwarden_linux_amd64\n")
	path := installed(t)
	if err := ReplaceBinary(context.Background(), base, "amd64", path, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatalf("binary = %q", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatal("replacement isn't executable")
	}
}

func TestReplaceBinaryRefusesATamperedBuild(t *testing.T) {
	fastRetries(t)
	base := release(t, "tampered", sumOf("new")+"  ezvaultwarden_linux_amd64\n")
	path := installed(t)
	err := ReplaceBinary(context.Background(), base, "amd64", path, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("got %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("binary = %q", got)
	}
}

func TestReplaceBinaryNeedsAChecksumForThisBuild(t *testing.T) {
	base := release(t, "new", sumOf("new")+"  ezvaultwarden_linux_arm64\n")
	path := installed(t)
	if err := ReplaceBinary(context.Background(), base, "amd64", path, nil); err == nil {
		t.Fatal("installed a build with no checksum")
	}
}
