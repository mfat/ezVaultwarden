package cli

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/mfat/ezVaultwarden/internal/cloudflare"
	"github.com/mfat/ezVaultwarden/internal/config"
	"github.com/mfat/ezVaultwarden/internal/fetch"
)

// dnsHost is the part of a DNS provider we need.
type dnsHost interface {
	Zone(ctx context.Context, host string) (string, error)
	Point(ctx context.Context, zone, host, ip string) error
	Remove(ctx context.Context, zone, host string) error
}

// The name reaches the Caddyfile and lego's arguments, so only a plain hostname
// gets through.
var validHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

// exposeLAN puts the vault on the user's own domain, for their network, using
// Let's Encrypt over Cloudflare DNS. It's hidden from users for now.
func (a *app) exposeLAN(ctx context.Context, s config.State, args []string) error {
	fs := flag.NewFlagSet("expose lan", flag.ContinueOnError)
	domain := fs.String("domain", "", "name for the vault, such as vault.example.com")
	dns := fs.String("dns", "cloudflare", "DNS provider that hosts the domain")
	assume := fs.Bool("yes", false, "don't ask for confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	host := strings.ToLower(*domain)
	if !validHost.MatchString(host) {
		return errors.New("name the vault with: ezvaultwarden expose lan --domain vault.example.com")
	}
	if *dns != "cloudflare" {
		return fmt.Errorf("%s isn't supported yet. Only cloudflare is", *dns)
	}
	if !s.SignupsClosed {
		return errors.New("signups are still open, so anyone who reaches this could register. Finish with: ezvaultwarden init")
	}
	if isPublic(a.localIP()) {
		return fmt.Errorf("this machine has a public address, %s, so this would put your vault login page "+
			"on the internet. Use this instead: ezvaultwarden expose tailnet", a.localIP())
	}
	fmt.Fprintf(a.out, "This makes your vault login page reachable from every device on your network,\n")
	fmt.Fprintf(a.out, "at https://%s:%d. It stays off the internet while this machine is behind a router.\n", host, s.Port)
	fmt.Fprintf(a.out, "The name %s goes into public certificate logs, and public DNS\n", host)
	fmt.Fprintln(a.out, "will show this machine's address.")
	fmt.Fprintln(a.out, "Continuing accepts Let's Encrypt's terms: https://letsencrypt.org/repository/")
	if !*assume {
		yes, err := a.confirm("Continue?", false)
		if err != nil {
			return err
		}
		if !yes {
			return errors.New("nothing changed")
		}
	}
	if err := a.saveDNSToken(ctx, host); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "  Requesting a certificate. This can take a minute or two...")
	if err := a.issue(ctx, host); err != nil {
		// A rejected token would otherwise be reused with no way to retype it.
		if !s.UsesLego() {
			if forgetErr := a.forgetDNSToken(); forgetErr != nil {
				a.warn("Couldn't delete the DNS token at %s: %v", a.paths.DNSToken(), forgetErr)
			}
		}
		return err
	}
	a.ok("Certificate issued for %s, renews automatically", host)
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := a.rt.Install(self); err != nil {
		return err
	}
	before := s
	if err := a.closeTailnet(ctx, before); err != nil {
		return err
	}
	s.Mode, s.Hostname = "lan", host
	if err := a.applyMode(ctx, s); err != nil {
		if undoErr := a.applyMode(ctx, before); undoErr != nil {
			a.warn("Couldn't go back to the previous address: %v", undoErr)
		}
		a.restoreTailnet(ctx, before)
		return err
	}
	a.ok("Available on your network at %s", s.URL())
	a.warn("Apps set to the old address need the new one. See: ezvaultwarden connect")
	a.pointDNS(ctx, s)
	fmt.Fprintf(a.out, "  If a firewall is on, allow port %d.\n", s.Port)
	return nil
}

// saveDNSToken keeps a token that works for host, owner-only, for renewals. A
// bad one is never saved, so a typo can't get stuck.
func (a *app) saveDNSToken(ctx context.Context, host string) error {
	if token, err := a.savedToken(); err == nil {
		if _, err := a.dns(token).Zone(ctx, host); err == nil {
			return nil
		}
		a.warn("The saved Cloudflare token doesn't work for %s.", host)
	}
	fmt.Fprintln(a.out, "ezVaultwarden needs a Cloudflare API token. On the page that opens, click")
	fmt.Fprintln(a.out, "Continue to summary, then Create Token. If this account has other domains, first")
	fmt.Fprintf(a.out, "set Zone Resources to the one that holds %s. Paste the token here:\n", host)
	fmt.Fprintf(a.out, "  %s\n", cloudflare.TokenURL)
	a.openLink(cloudflare.TokenURL)
	token, err := a.secret("Cloudflare API token: ")
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("no token, so nothing changed")
	}
	if _, err := a.dns(token).Zone(ctx, host); err != nil {
		return err
	}
	return config.WritePrivate(a.paths.DNSToken(), []byte(token))
}

func (a *app) savedToken() (string, error) {
	raw, err := os.ReadFile(a.paths.DNSToken())
	return strings.TrimSpace(string(raw)), err
}

func (a *app) forgetDNSToken() error {
	if err := os.Remove(a.paths.DNSToken()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// dropDNS clears up after lan mode: our DNS record, then the token. Failing here
// mustn't stop the user leaving the mode, so it only warns.
func (a *app) dropDNS(ctx context.Context, s config.State) {
	if token, err := a.savedToken(); err == nil && s.UsesLego() {
		dns := a.dns(token)
		zone, err := dns.Zone(ctx, s.Hostname)
		if err == nil {
			err = dns.Remove(ctx, zone, s.Hostname)
		}
		if err != nil {
			a.warn("Couldn't remove the DNS record for %s: %v", s.Hostname, err)
		}
	}
	if err := a.forgetDNSToken(); err != nil {
		a.warn("Couldn't delete the DNS token at %s: %v", a.paths.DNSToken(), err)
	}
}

func (a *app) syncDNS(ctx context.Context, s config.State, ip string) error {
	token, err := a.savedToken()
	if err != nil {
		return err
	}
	dns := a.dns(token)
	zone, err := dns.Zone(ctx, s.Hostname)
	if err != nil {
		return err
	}
	return dns.Point(ctx, zone, s.Hostname, ip)
}

// pointDNS sets the record. The vault already works locally by then, so a failure
// only means the user sets it by hand.
func (a *app) pointDNS(ctx context.Context, s config.State) {
	ip := a.localIP()
	if ip != "" {
		err := a.syncDNS(ctx, s, ip)
		if err == nil {
			a.ok("Cloudflare now points %s to this machine, %s", s.Hostname, ip)
			if !a.nameWorks(s.Hostname) {
				a.warnName(s.Hostname)
			}
			return
		}
		a.warn("Couldn't set the DNS record: %v", err)
	}
	fmt.Fprintln(a.out, "  In Cloudflare, add a DNS-only record:")
	fmt.Fprintf(a.out, "  A  %s  %s\n", s.Hostname, ip)
}

// nameWorks asks the way a client on this network would. Cloudflare holding the
// record proves nothing here: many routers drop public answers that hold a private
// address, as protection against DNS rebinding.
func (a *app) nameWorks(host string) bool {
	found, err := a.lookup(host)
	return err == nil && len(found) > 0
}

func (a *app) warnName(host string) {
	a.warn("This network can't look up %s, so devices here can't reach the vault by name.", host)
	fmt.Fprintln(a.out, "  A new name can take up to 30 minutes. If it stays like this, your router is")
	fmt.Fprintln(a.out, "  probably blocking it: turn off DNS rebinding protection, or add the name to")
	fmt.Fprintln(a.out, "  its local DNS. To go back instead, run: ezvaultwarden expose tailnet")
}

// openBrowser is a convenience: the link is printed too, for machines without one.
func openBrowser(url string) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	cmd := exec.Command("xdg-open", url)
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

// outboundIP finds the address this machine uses on its network. Dialing UDP
// sends nothing.
func outboundIP() string {
	conn, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return ""
}

// isPublic reports whether ip can be reached from the internet. 100.64.0.0/10 is
// carrier NAT and Tailscale. A cloud machine with a private address and a mapped
// public one looks private here, so the prompt avoids promising too much.
func isPublic(ip string) bool {
	addr := net.ParseIP(ip)
	if addr == nil || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	shared := net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	return !shared.Contains(addr)
}

// issueCert gets or renews the certificate. lego reads the token from its file,
// so it never shows up in a process list. Run again, it only acts when renewal
// is due.
func (a *app) issueCert(ctx context.Context, host string) error {
	lego, err := a.fetchLego(ctx)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, lego, legoArgs(host, a.paths.Lego())...)
	cmd.Env = append(os.Environ(), "CLOUDFLARE_DNS_API_TOKEN_FILE="+a.paths.DNSToken())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("the certificate request failed: %w\n%s", err, lastLines(out, 5))
	}
	return nil
}

// legoArgs waits a fixed time instead of letting lego look for the record itself.
// Networks that intercept DNS answer that lookup with a stale "no such name", so it
// could never succeed. Let's Encrypt checks from outside, and Cloudflare is quick.
func legoArgs(host, path string) []string {
	return []string{"run", "--accept-tos", "--dns", "cloudflare", "--dns.propagation.wait", "30s",
		"--domains", host, "--path", path, "--log.format", "text"}
}

func lastLines(out []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// fetchLego returns the pinned lego, downloading it the first time. As with
// versions/, the folder only appears once complete and verified.
func (a *app) fetchLego(ctx context.Context) (string, error) {
	final := filepath.Join(a.paths.Tools(), "lego-"+fetch.LegoVersion)
	binary := filepath.Join(final, "lego")
	if _, err := os.Stat(final); err == nil {
		return binary, nil
	}
	url, sum, err := fetch.LegoRelease(goruntime.GOARCH)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(a.paths.Tools(), 0o700); err != nil {
		return "", err
	}
	staged, err := os.MkdirTemp(a.paths.Tools(), ".download-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	if err := fetch.ExtractLego(ctx, url, sum, staged, a.progress("lego")); err != nil {
		return "", fmt.Errorf("downloading lego: %w", err)
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return "", err
	}
	return binary, os.Rename(staged, final)
}

// renewCert runs hourly, so an address change is followed quickly. The certificate
// itself is only checked daily. Caddy only reads the files at start, so it
// restarts when they change.
func (a *app) renewCert([]string) error {
	s, err := a.load()
	if err != nil {
		return err
	}
	if !s.UsesLego() {
		return nil
	}
	if ip := a.localIP(); ip != "" {
		if err := a.syncDNS(context.Background(), s, ip); err != nil {
			a.warn("Couldn't update the DNS record: %v", err)
		}
	}
	if !a.certCheckDue() {
		return nil
	}
	before, err := os.ReadFile(a.paths.CertFile(s.Hostname))
	if err != nil {
		return err
	}
	if err := a.issue(context.Background(), s.Hostname); err != nil {
		return err
	}
	stamp := a.now().Format(time.RFC3339)
	if err := config.WritePrivate(a.paths.CertCheck(), []byte(stamp)); err != nil {
		return err
	}
	after, err := os.ReadFile(a.paths.CertFile(s.Hostname))
	if err != nil {
		return err
	}
	if bytes.Equal(before, after) {
		return nil
	}
	return a.rt.RestartProxy()
}

func (a *app) certCheckDue() bool {
	raw, err := os.ReadFile(a.paths.CertCheck())
	if err != nil {
		return true
	}
	last, err := time.Parse(time.RFC3339, string(raw))
	return err != nil || a.now().Sub(last) >= 23*time.Hour
}

// certProblem warns before an expired certificate locks every client out. A
// healthy certificate renews weeks ahead, so two weeks left means renewal is
// failing.
func (a *app) certProblem(s config.State) string {
	if !s.UsesLego() {
		return ""
	}
	raw, err := os.ReadFile(a.paths.CertFile(s.Hostname))
	if err != nil {
		return "The certificate is missing. Fix it with: ezvaultwarden expose lan --domain " + s.Hostname
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "The certificate can't be read: " + a.paths.CertFile(s.Hostname)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "The certificate can't be read: " + a.paths.CertFile(s.Hostname)
	}
	if left := cert.NotAfter.Sub(a.now()); left < 14*24*time.Hour {
		return fmt.Sprintf("The certificate expires on %s and isn't renewing. See: journalctl --user -u ezvaultwarden-cert",
			cert.NotAfter.Format("2006-01-02"))
	}
	return ""
}
