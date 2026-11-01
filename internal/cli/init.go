package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mfat/ezVaultwarden/internal/backup"
	"github.com/mfat/ezVaultwarden/internal/config"
	"github.com/mfat/ezVaultwarden/internal/fetch"
	"github.com/mfat/ezVaultwarden/internal/vault"
)

type initFlags struct {
	port   int
	assume bool // answer yes to everything, for scripts
}

func (a *app) init(args []string) error {
	var f initFlags
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.IntVar(&f.port, "port", 8443, "port for the web vault")
	fs.BoolVar(&f.assume, "yes", false, "don't ask questions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	s, err := a.load()
	switch {
	case errors.Is(err, config.ErrNotSetUp):
		if s, err = a.install(ctx, f); err != nil {
			return err
		}
	case err != nil:
		return err
	case s.SignupsClosed:
		return fmt.Errorf("already set up at %s. See: ezvaultwarden status", s.URL())
	default:
		fmt.Fprintln(a.out, "Picking up where setup left off.")
	}
	return a.finish(ctx, s, f)
}

func (a *app) install(ctx context.Context, f initFlags) (config.State, error) {
	var s config.State
	if goruntime.GOOS != "linux" {
		return s, errors.New("native mode runs on Linux. Docker and Podman support is coming")
	}
	if err := a.rt.Available(); err != nil {
		return s, err
	}
	backendPort := f.port + 10000
	for _, port := range []int{f.port, backendPort} {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return s, fmt.Errorf("port %d is in use. Pick another with: ezvaultwarden init --port N", port)
		}
		l.Close()
	}
	fmt.Fprintf(a.out, "ezVaultwarden %s\n\n", Version)
	for _, dir := range []string{a.paths.Root, a.paths.Data(), a.paths.Backups(), a.paths.Versions(), a.paths.CaddyData()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return s, err
		}
	}
	a.ok("%s/%s, running natively", goruntime.GOOS, goruntime.GOARCH)
	fmt.Fprintln(a.out, "  Downloading Vaultwarden and Caddy...")
	if err := a.download(ctx); err != nil {
		return s, err
	}
	a.ok("Vaultwarden %s, web vault and Caddy %s downloaded and verified",
		fetch.VaultwardenVersion, fetch.CaddyVersion)

	recipient, secret, err := backup.NewKey()
	if err != nil {
		return s, err
	}
	// Kept only until the user confirms saving it, so an interrupted setup
	// can't lose the one copy.
	if err := config.WritePrivate(a.paths.PendingKey(), []byte(secret+"\n")); err != nil {
		return s, err
	}
	s = config.State{
		Version: fetch.VaultwardenVersion, Runtime: a.rt.Name(), Mode: "local",
		Port: f.port, BackendPort: backendPort, BackupRecipient: recipient,
	}
	if err := config.Write(a.paths, s); err != nil {
		return s, err
	}
	if err := config.Save(a.paths, s); err != nil {
		return s, err
	}
	self, err := os.Executable()
	if err != nil {
		return s, err
	}
	if err := a.rt.Install(self); err != nil {
		return s, err
	}
	if err := a.health(ctx, s); err != nil {
		return s, err
	}
	a.ok("Service started at %s", s.URL())
	return s, nil
}

func (a *app) download(ctx context.Context) error {
	dir, err := a.fetchVersion(ctx)
	if err != nil {
		return err
	}
	return a.setCurrent(dir)
}

// fetchVersion returns the folder holding this build's components, downloading
// them unless an earlier run did. The folder only appears once complete and
// verified, so finding it is proof enough.
func (a *app) fetchVersion(ctx context.Context) (string, error) {
	final := filepath.Join(a.paths.Versions(), fetch.VersionID())
	if _, err := os.Stat(final); err == nil {
		return final, nil
	}
	url, sum, err := fetch.CaddyRelease(goruntime.GOARCH)
	if err != nil {
		return "", err
	}
	staged, err := os.MkdirTemp(a.paths.Versions(), ".download-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	if err := fetch.ExtractVaultwarden(ctx, fetch.VaultwardenImage, goruntime.GOARCH, staged, a.progress("Vaultwarden")); err != nil {
		return "", fmt.Errorf("downloading Vaultwarden: %w", err)
	}
	if err := fetch.ExtractCaddy(ctx, url, sum, staged, a.progress("Caddy")); err != nil {
		return "", fmt.Errorf("downloading Caddy: %w", err)
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return "", err
	}
	return final, os.Rename(staged, final)
}

// setCurrent repoints the services in one atomic step.
func (a *app) setCurrent(dir string) error {
	link := a.paths.Current() + ".new"
	os.Remove(link)
	if err := os.Symlink(dir, link); err != nil {
		return err
	}
	return os.Rename(link, a.paths.Current())
}

// progress shows that a slow download is still moving, every 5 MB so it stays
// readable in a log as well as a terminal.
func (a *app) progress(name string) fetch.Report {
	const step = 5 << 20
	var shown int64
	return func(received int64) {
		if received/step > shown {
			shown = received / step
			fmt.Fprintf(a.out, "  %s: %d MB\n", name, received>>20)
		}
	}
}

// verify is what counts as a working install: the binary in place is the one
// the state names, and it answers through the front end.
func (a *app) verify(ctx context.Context, s config.State) error {
	out, err := exec.CommandContext(ctx, filepath.Join(a.paths.Current(), "vaultwarden"), "--version").Output()
	if err != nil {
		return fmt.Errorf("the server binary doesn't run: %w", err)
	}
	if !strings.Contains(string(out), s.Version) {
		return fmt.Errorf("expected Vaultwarden %s, found %s", s.Version, strings.TrimSpace(string(out)))
	}
	return a.waitHealthy(ctx, s)
}

// waitHealthy proves the whole path: Caddy answers over TLS with a certificate
// from our own CA, and reaches the server behind it.
func (a *app) waitHealthy(ctx context.Context, s config.State) error {
	deadline := time.Now().Add(45 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if last = a.probe(ctx, s); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("the server didn't come up (%v). See: ezvaultwarden logs", last)
}

func (a *app) probe(ctx context.Context, s config.State) error {
	pem, err := os.ReadFile(a.paths.RootCert())
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return errors.New("bad local certificate")
	}
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL()+"/alive", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check: %s", resp.Status)
	}
	return nil
}

func (a *app) finish(ctx context.Context, s config.State, f initFlags) error {
	email, err := a.waitForAccount(ctx, s)
	if err != nil {
		return err
	}
	a.ok("Account created: %s", email)
	s.SignupsClosed = true
	if err := config.Write(a.paths, s); err != nil {
		return err
	}
	if err := config.Save(a.paths, s); err != nil {
		return err
	}
	if err := a.rt.RestartVault(); err != nil {
		return err
	}
	if err := a.health(ctx, s); err != nil {
		return err
	}
	a.ok("Signups closed, nobody else can register")
	if err := a.showRecoveryKit(s, email, f); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "\nConnect a client:   ezvaultwarden connect")
	return nil
}

func (a *app) waitForAccount(ctx context.Context, s config.State) (string, error) {
	fmt.Fprintf(a.out, "\nOpen %s and create your account.\n", s.URL())
	fmt.Fprintf(a.out, "Your browser will warn you until you import %s into its certificate settings.\n", a.paths.RootCert())
	fmt.Fprintln(a.out, "Waiting for your account...")
	for {
		emails, err := vault.Accounts(a.paths.Data())
		if err == nil && len(emails) > 0 {
			return emails[0], nil
		}
		select {
		case <-ctx.Done():
			return "", errors.New("stopped before an account was created. Signups are still open; " +
				"run ezvaultwarden init again to finish")
		case <-time.After(time.Second):
		}
	}
}

func (a *app) showRecoveryKit(s config.State, email string, f initFlags) error {
	raw, err := os.ReadFile(a.paths.PendingKey())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	key := strings.TrimSpace(string(raw))
	fmt.Fprintf(a.out, `
Your recovery kit. Print it or write it down, and keep it somewhere safe.

  Server          %s
  Account         %s
  Master password ______________________________
  Backup key      %s

Nobody can recover your master password, including this tool.
Without the backup key, your backups can't be restored.

`, s.URL(), email, key)
	if !f.assume {
		for {
			saved, err := a.confirm("Saved it?", false)
			if err != nil {
				return err
			}
			if saved {
				break
			}
			fmt.Fprintln(a.out, "This is the only time it's shown. Please save it first.")
		}
		return os.Remove(a.paths.PendingKey())
	}
	fmt.Fprintf(a.out, "Running with --yes, so the key stays in %s until you move it.\n", a.paths.PendingKey())
	return nil
}
