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

type fakeDNS struct {
	zoneErr  error
	pointErr error
	tokens   []string
	points   []string
	removed  int
	opened   int
}

func (f *fakeDNS) Zone(context.Context, string) (string, error) { return "zone1", f.zoneErr }
func (f *fakeDNS) Point(_ context.Context, _, host, ip string) error {
	if f.pointErr == nil {
		f.points = append(f.points, host+"="+ip)
	}
	return f.pointErr
}
func (f *fakeDNS) Remove(context.Context, string, string) error { f.removed++; return nil }

func lanSetup(t *testing.T, input string) (*harness, *fakeTailnet, *int) {
	h, ts, _, issued := lanSetupDNS(t, input)
	return h, ts, issued
}

func lanSetupDNS(t *testing.T, input string) (*harness, *fakeTailnet, *fakeDNS, *int) {
	t.Helper()
	h, ts := exposeSetup(t)
	h.in = reader(input)
	dns := &fakeDNS{}
	h.dns = func(token string) dnsHost {
		dns.tokens = append(dns.tokens, token)
		return dns
	}
	h.openLink = func(string) { dns.opened++ }
	h.lookup = func(string) ([]string, error) { return []string{"192.168.1.20"}, nil }
	issued := new(int)
	h.issue = func(_ context.Context, host string) error {
		*issued++
		writeCert(t, h, host, 60*24*time.Hour)
		return nil
	}
	return h, ts, dns, issued
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

func TestTokenIsCheckedBeforeItIsSaved(t *testing.T) {
	h, _, dns, issued := lanSetupDNS(t, "y\nbad-token\n")
	dns.zoneErr = errors.New("refused")
	if err := h.expose(lanArgs()); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(h.paths.DNSToken()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a bad token was saved: %v", err)
	}
	if *issued != 0 {
		t.Fatal("asked for a certificate with a bad token")
	}
}

func TestSetupOpensTheTokenPageOnce(t *testing.T) {
	h, _, dns, _ := lanSetupDNS(t, "y\ntoken\n")
	if err := h.expose(lanArgs()); err != nil {
		t.Fatal(err)
	}
	if dns.opened != 1 || !strings.Contains(h.output.String(), "dash.cloudflare.com") {
		t.Fatalf("opened %d: %s", dns.opened, h.output)
	}
}

func TestAWorkingSavedTokenIsReused(t *testing.T) {
	h, _, dns, _ := lanSetupDNS(t, "y\n")
	if err := config.WritePrivate(h.paths.DNSToken(), []byte("saved\n")); err != nil {
		t.Fatal(err)
	}
	if err := h.expose(lanArgs()); err != nil {
		t.Fatal(err)
	}
	if dns.opened != 0 || dns.tokens[0] != "saved" {
		t.Fatalf("opened %d, tokens %v", dns.opened, dns.tokens)
	}
}

func TestExposeLANSetsTheDNSRecord(t *testing.T) {
	h, _, dns, _ := lanSetupDNS(t, "y\ntoken\n")
	if err := h.expose(lanArgs()); err != nil {
		t.Fatal(err)
	}
	if outboundIP() != "" && (len(dns.points) != 1 || !strings.HasPrefix(dns.points[0], domain+"=")) {
		t.Fatalf("points: %v", dns.points)
	}
}

func TestABlockedNameIsReportedInsteadOfCelebrated(t *testing.T) {
	if outboundIP() == "" {
		t.Skip("no network")
	}
	h, _, _, _ := lanSetupDNS(t, "y\ntoken\n")
	h.lookup = func(string) ([]string, error) { return nil, errors.New("no such host") }
	if err := h.expose(lanArgs()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "can't look up") {
		t.Fatalf("no warning: %s", h.output)
	}
	h.output.Reset()
	if err := h.status(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "rebinding") {
		t.Fatalf("status stayed quiet: %s", h.output)
	}
}

func TestAFailedDNSRecordFallsBackToInstructions(t *testing.T) {
	if outboundIP() == "" {
		t.Skip("no network")
	}
	h, _, dns, _ := lanSetupDNS(t, "y\ntoken\n")
	dns.pointErr = errors.New("left alone")
	if err := h.expose(lanArgs()); err != nil {
		t.Fatalf("the vault works, so this isn't a failure: %v", err)
	}
	if !strings.Contains(h.output.String(), "add a DNS-only record") {
		t.Fatalf("no instructions: %s", h.output)
	}
}

func TestLeavingLANRemovesTheRecordAndToken(t *testing.T) {
	for _, mode := range []string{"local", "tailnet"} {
		h, _, dns, _ := lanSetupDNS(t, "y\ntoken\n")
		if err := h.expose(lanArgs()); err != nil {
			t.Fatal(err)
		}
		if err := h.expose([]string{mode}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(h.paths.DNSToken()); dns.removed != 1 || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: removed %d, token: %v", mode, dns.removed, err)
		}
	}
}

func TestRenewalFollowsTheAddressHourlyButChecksTheCertDaily(t *testing.T) {
	if outboundIP() == "" {
		t.Skip("no network")
	}
	h, _, dns, issued := lanSetupDNS(t, "y\ntoken\n")
	if err := h.expose(lanArgs()); err != nil {
		t.Fatal(err)
	}
	dns.points, *issued = nil, 0
	for range 3 {
		if err := h.renewCert(nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dns.points) != 3 || *issued != 1 {
		t.Fatalf("points %d, issued %d", len(dns.points), *issued)
	}
	h.now = func() time.Time { return time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC) }
	if err := h.renewCert(nil); err != nil {
		t.Fatal(err)
	}
	if *issued != 2 {
		t.Fatalf("issued %d the next day", *issued)
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

func TestCertificatesDontDependOnLocalDNS(t *testing.T) {
	args := strings.Join(legoArgs(domain, "/data"), " ")
	if !strings.Contains(args, "--dns.propagation.wait 30s") {
		t.Fatalf("a network that intercepts DNS would break this: %s", args)
	}
}
