package runtime

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/mfat/ezVaultwarden/internal/config"
)

const (
	vaultUnit  = "ezvaultwarden-server.service"
	caddyUnit  = "ezvaultwarden-proxy.service"
	backupUnit = "ezvaultwarden-backup.service"
	timerUnit  = "ezvaultwarden-backup.timer"
	certUnit   = "ezvaultwarden-cert.service"
	certTimer  = "ezvaultwarden-cert.timer"
)

// Systemd runs the static binaries as user services, so nothing needs root.
type Systemd struct{ Paths config.Paths }

// Name implements Runtime.
func (Systemd) Name() string { return "native" }

// Available implements Runtime.
func (Systemd) Available() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemd was not found; Docker and Podman support is coming")
	}
	out, _ := exec.Command("systemctl", "--user", "is-system-running").Output()
	switch strings.TrimSpace(string(out)) {
	case "running", "degraded", "starting", "initializing":
		return nil
	}
	return errors.New("your user systemd session isn't running")
}

func unitDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// Units renders the unit files by name.
func Units(p config.Paths, self string) map[string]string {
	current := p.Current()
	hardening := "NoNewPrivileges=true\nUMask=0077\nRestart=on-failure\nRestartSec=3\n"
	return map[string]string{
		vaultUnit: fmt.Sprintf(`[Unit]
Description=ezVaultwarden server

[Service]
EnvironmentFile=%s
WorkingDirectory=%s
ExecStart="%s"
%s
[Install]
WantedBy=default.target
`, p.VaultEnv(), p.Root, filepath.Join(current, "vaultwarden"), hardening),
		caddyUnit: fmt.Sprintf(`[Unit]
Description=ezVaultwarden TLS proxy
After=%s

[Service]
ExecStart="%s" run --config "%s" --adapter caddyfile
%s
[Install]
WantedBy=default.target
`, vaultUnit, filepath.Join(current, "caddy"), p.Caddyfile(), hardening),
		backupUnit: fmt.Sprintf(`[Unit]
Description=ezVaultwarden daily backup

[Service]
Type=oneshot
Environment=EZVAULTWARDEN_HOME=%s
ExecStart="%s" backup --auto
`, p.Root, self),
		timerUnit: `[Unit]
Description=ezVaultwarden daily backup

[Timer]
OnCalendar=03:00
RandomizedDelaySec=30m
Persistent=true

[Install]
WantedBy=timers.target
`,
		certUnit: fmt.Sprintf(`[Unit]
Description=ezVaultwarden certificate renewal

[Service]
Type=oneshot
Environment=EZVAULTWARDEN_HOME=%s
ExecStart="%s" renew-cert
`, p.Root, self),
		certTimer: `[Unit]
Description=ezVaultwarden certificate renewal

[Timer]
OnCalendar=hourly
RandomizedDelaySec=5m
Persistent=true

[Install]
WantedBy=timers.target
`,
	}
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("systemctl %s: %s", args[0], detail)
		}
		return fmt.Errorf("systemctl %s: %w", args[0], err)
	}
	return nil
}

// Install implements Runtime.
func (s Systemd) Install(self string) error {
	dir, err := unitDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, body := range Units(s.Paths, self) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", vaultUnit, caddyUnit, timerUnit, certTimer); err != nil {
		return err
	}
	s.keepRunningAfterLogout()
	return nil
}

func (s Systemd) lingerMarker() string { return filepath.Join(s.Paths.Root, "linger-enabled") }

// keepRunningAfterLogout is best effort: without it the vault stops with the
// last session, but setup still works. A marker records that the setting was
// ours, so Remove only undoes a change this tool made.
func (s Systemd) keepRunningAfterLogout() {
	u, err := user.Current()
	if err != nil {
		return
	}
	out, _ := exec.Command("loginctl", "show-user", u.Username, "-p", "Linger").Output()
	if strings.TrimSpace(string(out)) == "Linger=yes" {
		return
	}
	if exec.Command("loginctl", "enable-linger", u.Username).Run() == nil {
		_ = os.WriteFile(s.lingerMarker(), nil, 0o600)
	}
}

func (s Systemd) restoreLinger() {
	if _, err := os.Stat(s.lingerMarker()); err != nil {
		return
	}
	if u, err := user.Current(); err == nil {
		_ = exec.Command("loginctl", "disable-linger", u.Username).Run()
	}
	_ = os.Remove(s.lingerMarker())
}

// Start implements Runtime.
func (Systemd) Start() error { return systemctl("start", vaultUnit, caddyUnit) }

// Stop implements Runtime.
func (Systemd) Stop() error { return systemctl("stop", caddyUnit, vaultUnit) }

// StartVault implements Runtime.
func (Systemd) StartVault() error { return systemctl("start", vaultUnit) }

// StopVault implements Runtime.
func (Systemd) StopVault() error { return systemctl("stop", vaultUnit) }

// RestartVault implements Runtime.
func (Systemd) RestartVault() error { return systemctl("restart", vaultUnit) }

// RestartProxy implements Runtime.
func (Systemd) RestartProxy() error { return systemctl("restart", caddyUnit) }

// Running implements Runtime.
func (Systemd) Running() bool {
	// One unit at a time: given several, is-active succeeds if any is up.
	return systemctl("is-active", "--quiet", vaultUnit) == nil &&
		systemctl("is-active", "--quiet", caddyUnit) == nil
}

// Logs implements Runtime.
func (Systemd) Logs(w io.Writer, n int) error {
	cmd := exec.Command("journalctl", "--user", "--no-pager", "-n", fmt.Sprint(n),
		"-u", vaultUnit, "-u", caddyUnit, "-u", backupUnit)
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// Remove implements Runtime.
func (s Systemd) Remove() error {
	// One at a time: a unit this install never had, such as the certificate timer on
	// an install from before it existed, would otherwise stop all of them failing.
	for _, unit := range []string{certTimer, timerUnit, caddyUnit, vaultUnit} {
		_ = systemctl("disable", "--now", unit)
	}
	s.restoreLinger()
	dir, err := unitDir()
	if err != nil {
		return err
	}
	for _, name := range []string{vaultUnit, caddyUnit, backupUnit, timerUnit, certUnit, certTimer} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return systemctl("daemon-reload")
}
