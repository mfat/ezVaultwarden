package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mfat/ezVaultwarden/internal/config"
)

const domain = "vault.example.com"

func lanSetup(t *testing.T, input string) (*harness, *fakeTailnet, *int) {
	t.Helper()
	h, ts := exposeSetup(t)
	h.in = reader(input)
	issued := new(int)
	h.issue = func(_ context.Context, host string) error {
		*issued++
		writeCert(t, h, host, 60*24*time.Hour)
		return nil
	}
	return h, ts, issued
}

// writeCert plants a certificate as lego would leave it.
func writeCert(t *testing.T, h *harness, host string, life time.Duration) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotAfter: h.now().Add(life)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := h.paths.CertFile(host)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lanArgs() []string { return []string{"lan", "--domain", domain} }

func TestExposeLANMovesTheVaultToItsDomain(t *testing.T) {
	h, _, issued := lanSetup(t, "y\nsecret-token\n")
	if err := h.expose(lanArgs()); err != nil {
		t.Fatal(err)
	}
	s, _ := h.load()
	if *issued != 1 || s.Mode != "lan" || s.URL() != "https://"+domain+":8443" {
		t.Fatalf("issued %d, state %+v", *issued, s)
	}
	if !strings.Contains(env(t, h), "DOMAIN=https://"+domain+":8443\n") {
		t.Fatalf("DOMAIN must match the client URL:\n%s", env(t, h))
	}
	caddy, _ := os.ReadFile(h.paths.Caddyfile())
	if strings.Contains(string(caddy), "bind 127.0.0.1") || !strings.Contains(string(caddy), h.paths.CertFile(domain)) {
		t.Fatalf("Caddyfile:\n%s", caddy)
	}
	info, err := os.Stat(h.paths.DNSToken())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v, %v", info, err)
	}
	if strings.Contains(h.output.String(), "secret-token") {
		t.Fatal("printed the token")
	}
}

func TestExposeLANNeedsAYes(t *testing.T) {
	h, _, issued := lanSetup(t, "\n")
	if err := h.expose(lanArgs()); err == nil {
		t.Fatal("expected a refusal")
	}
	if s, _ := h.load(); *issued != 0 || s.Mode != "local" {
		t.Fatalf("changed something: %d %+v", *issued, s)
	}
}

func TestExposeLANRefusesWhileSignupsAreOpen(t *testing.T) {
	h, _, issued := lanSetup(t, "")
	s, _ := h.load()
	s.SignupsClosed = false
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
	if err := h.expose(append(lanArgs(), "--yes")); err == nil || *issued != 0 {
		t.Fatalf("err %v, issued %d", err, *issued)
	}
}

func TestBadDomainsNeverReachTheCaddyfile(t *testing.T) {
	for _, bad := range []string{"", "localhost", "a b.com", "x.com\n}", "--dns.com", "x.com;rm", "*.example.com"} {
		h, _, issued := lanSetup(t, "y\ntoken\n")
		if err := h.expose([]string{"lan", "--domain", bad}); err == nil || *issued != 0 {
			t.Errorf("%q: err %v, issued %d", bad, err, *issued)
		}
	}
}

func TestFailedCertificateChangesNothing(t *testing.T) {
	h, _, _ := lanSetup(t, "y\ntoken\n")
	h.issue = func(context.Context, string) error { return errors.New("refused") }
	if err := h.expose(lanArgs()); err == nil {
		t.Fatal("expected an error")
	}
	if s, _ := h.load(); s.Mode != "local" {
		t.Fatalf("mode changed to %s", s.Mode)
	}
	if _, err := os.Stat(h.paths.DNSToken()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected token can't be retyped: %v", err)
	}
}

func TestUnhealthyServerGoesBackFromLAN(t *testing.T) {
	h, _, _ := lanSetup(t, "y\ntoken\n")
	h.health = func(_ context.Context, s config.State) error {
		if s.Mode == "lan" {
			return errors.New("down")
		}
		return nil
	}
	if err := h.expose(lanArgs()); err == nil {
		t.Fatal("expected an error")
	}
	if s, _ := h.load(); s.Mode != "local" || s.Hostname != "" {
		t.Fatalf("not undone: %+v", s)
	}
	caddy, _ := os.ReadFile(h.paths.Caddyfile())
	if !strings.Contains(string(caddy), "bind 127.0.0.1") {
		t.Fatalf("still on the network:\n%s", caddy)
	}
}

func TestMovingFromTailnetToLANStopsTailscale(t *testing.T) {
	h, ts, _ := lanSetup(t, "y\ntoken\n")
	if err := h.expose([]string{"tailnet"}); err != nil {
		t.Fatal(err)
	}
	if err := h.expose(lanArgs()); err != nil {
		t.Fatal(err)
	}
	if ts.stopped != 1 {
		t.Fatal("Tailscale still forwards to the vault")
	}
}

func TestExposeLocalForgetsTheToken(t *testing.T) {
	h, ts, _ := lanSetup(t, "y\ntoken\n")
	if err := h.expose(lanArgs()); err != nil {
		t.Fatal(err)
	}
	if err := h.expose([]string{"local"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.paths.DNSToken()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token kept: %v", err)
	}
	if ts.stopped != 0 {
		t.Fatal("touched Tailscale, which wasn't in use")
	}
	caddy, _ := os.ReadFile(h.paths.Caddyfile())
	if !strings.Contains(string(caddy), "bind 127.0.0.1") {
		t.Fatalf("still on the network:\n%s", caddy)
	}
}

func TestUninstallForgetsTheToken(t *testing.T) {
	h, _, _ := lanSetup(t, "y\ntoken\n")
	if err := h.expose(lanArgs()); err != nil {
		t.Fatal(err)
	}
	if err := h.uninstall(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.paths.DNSToken()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token kept: %v", err)
	}
}

func TestRenewalDoesNothingOutsideLANMode(t *testing.T) {
	h, _, issued := lanSetup(t, "")
	if err := h.renewCert(nil); err != nil || *issued != 0 {
		t.Fatalf("err %v, issued %d", err, *issued)
	}
}

func TestStatusWarnsWhenRenewalIsFailing(t *testing.T) {
	h, _, _ := lanSetup(t, "y\ntoken\n")
	if err := h.expose(lanArgs()); err != nil {
		t.Fatal(err)
	}
	s, _ := h.load()
	if p := h.certProblem(s); p != "" {
		t.Fatalf("healthy certificate: %s", p)
	}
	writeCert(t, h, domain, 5*24*time.Hour)
	if p := h.certProblem(s); !strings.Contains(p, "isn't renewing") {
		t.Fatalf("got %q", p)
	}
}
