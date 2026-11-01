// Package config owns the on-disk layout, the saved state and the config files
// generated from it. Everything stays plain text in one folder, so a user can
// read it, and leave the tool without losing anything.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Paths is the layout under one root folder.
type Paths struct{ Root string }

// DefaultPaths honours EZVAULTWARDEN_HOME, then XDG_DATA_HOME.
func DefaultPaths() (Paths, error) {
	if root := os.Getenv("EZVAULTWARDEN_HOME"); root != "" {
		return Paths{Root: root}, nil
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return Paths{Root: filepath.Join(xdg, "ezvaultwarden")}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return Paths{Root: filepath.Join(home, ".local", "share", "ezvaultwarden")}, nil
}

func (p Paths) join(elem ...string) string {
	return filepath.Join(append([]string{p.Root}, elem...)...)
}

func (p Paths) State() string      { return p.join("state.json") }
func (p Paths) Snapshot() string   { return p.join("data.before-update") }
func (p Paths) Data() string       { return p.join("data") }
func (p Paths) Backups() string    { return p.join("backups") }
func (p Paths) Versions() string   { return p.join("versions") }
func (p Paths) Current() string    { return p.join("current") }
func (p Paths) CaddyData() string  { return p.join("caddy") }
func (p Paths) Caddyfile() string  { return p.join("Caddyfile") }
func (p Paths) VaultEnv() string   { return p.join("vaultwarden.env") }
func (p Paths) PendingKey() string { return p.join("backup-key.pending") }
func (p Paths) RootCert() string {
	return p.join("caddy", "pki", "authorities", "local", "root.crt")
}

// State is everything chosen at setup.
type State struct {
	Previous        *Previous `json:"previous,omitempty"`
	Version         string    `json:"version"`
	Runtime         string    `json:"runtime"`
	Mode            string    `json:"mode"`
	Hostname        string    `json:"hostname,omitempty"`
	Port            int       `json:"port"`
	BackendPort     int       `json:"backend_port"`
	SignupsClosed   bool      `json:"signups_closed"`
	BackupRecipient string    `json:"backup_recipient"`
	BackupTargets   []string  `json:"backup_targets,omitempty"`

	// AdminToken is set only while the admin page is open, and never saved.
	AdminToken string `json:"-"`
}

// Previous is what rollback returns to.
type Previous struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// URL is the address clients use.
func (s State) URL() string {
	if s.Mode == "tailnet" {
		return "https://" + s.Hostname
	}
	return s.LocalURL()
}

// LocalURL is Caddy on this machine, which answers in every mode.
func (s State) LocalURL() string { return fmt.Sprintf("https://localhost:%d", s.Port) }

// ErrNotSetUp means init has not run.
var ErrNotSetUp = errors.New("ezVaultwarden isn't set up yet. Run: ezvaultwarden init")

// Load reads the saved state.
func Load(p Paths) (State, error) {
	var s State
	raw, err := os.ReadFile(p.State())
	if errors.Is(err, fs.ErrNotExist) {
		return s, ErrNotSetUp
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(raw, &s)
}

// Save writes the state atomically.
func Save(p Paths, s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivate(p.State(), append(raw, '\n'))
}

// WritePrivate replaces path atomically with an owner-only file.
func WritePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// VaultwardenEnv renders the server's settings. The server listens on loopback
// only; Caddy is the single way in.
func VaultwardenEnv(p Paths, s State) string {
	lines := []string{
		"ROCKET_ADDRESS=127.0.0.1",
		fmt.Sprintf("ROCKET_PORT=%d", s.BackendPort),
		"DATA_FOLDER=" + p.Data(),
		"WEB_VAULT_ENABLED=true",
		"WEB_VAULT_FOLDER=" + filepath.Join(p.Current(), "web-vault"),
		"DOMAIN=" + s.URL(),
		fmt.Sprintf("SIGNUPS_ALLOWED=%t", !s.SignupsClosed),
		"INVITATIONS_ALLOWED=false",
		"SHOW_PASSWORD_HINT=false",
	}
	if s.AdminToken != "" {
		// Quoted: a hash is full of $, which must reach Vaultwarden unexpanded.
		lines = append(lines, "ADMIN_TOKEN='"+s.AdminToken+"'")
	}
	return strings.Join(lines, "\n") + "\n"
}

// Caddyfile renders the TLS front end for local mode.
func Caddyfile(p Paths, s State) string {
	return fmt.Sprintf(`{
	admin off
	persist_config off
	skip_install_trust
	auto_https disable_redirects
	storage file_system {
		root %q
	}
}

https://localhost:%d {
	bind 127.0.0.1
	tls internal
	reverse_proxy 127.0.0.1:%d
}
`, p.CaddyData(), s.Port, s.BackendPort)
}

// Write renders every generated file from the state.
func Write(p Paths, s State) error {
	if err := WritePrivate(p.VaultEnv(), []byte(VaultwardenEnv(p, s))); err != nil {
		return err
	}
	return WritePrivate(p.Caddyfile(), []byte(Caddyfile(p, s)))
}
