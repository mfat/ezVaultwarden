package fetch

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ReleaseURL is where released builds live. EZVAULTWARDEN_RELEASE_URL replaces
// it, as a staging release during testing.
func ReleaseURL() string {
	if u := os.Getenv("EZVAULTWARDEN_RELEASE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://github.com/mfat/ezVaultwarden/releases/latest/download"
}

func getSmall(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return string(body), err
}

// LatestVersion returns the newest released version.
func LatestVersion(ctx context.Context, base string) (string, error) {
	body, err := getSmall(ctx, base+"/VERSION")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(body), nil
}

// Newer reports whether version a is later than b. Anything that isn't a
// release version, such as a development build, never counts as older.
func Newer(a, b string) bool {
	x, okA := parseVersion(a)
	y, okB := parseVersion(b)
	if !okA || !okB {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ReplaceBinary swaps the running program for the released build, verified
// against the release's checksums. The rename means a failure at any point
// leaves the old program in place.
func ReplaceBinary(ctx context.Context, base, arch, path string, report Report) error {
	asset := "ezvaultwarden_linux_" + arch
	sums, err := getSmall(ctx, base+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("downloading checksums: %w", err)
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return err
	}
	open := func(ctx context.Context, offset int64) (*http.Response, error) {
		req, err := rangeRequest(ctx, base+"/"+asset, offset)
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}
	f, err := download(ctx, open, func(r io.Reader) error {
		h := sha256.New()
		if _, err := io.Copy(h, r); err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return fmt.Errorf("checksum mismatch for %s", asset)
		}
		return nil
	}, report)
	if err != nil {
		return err
	}
	defer f.Close()
	tmp := filepath.Join(filepath.Dir(path), ".ezvaultwarden.new")
	if err := writeFile(tmp, f, 0o755); err != nil {
		return fmt.Errorf("can't replace %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("can't replace %s: %w", path, err)
	}
	return nil
}

func checksumFor(sums, asset string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("the release lists no checksum for %s", asset)
}
