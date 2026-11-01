// Package cli is the command line. Every command explains what happened and
// what to do next, in plain language.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/mfat/ezVaultwarden/internal/backup"
	"github.com/mfat/ezVaultwarden/internal/cloudflare"
	"github.com/mfat/ezVaultwarden/internal/config"
	"github.com/mfat/ezVaultwarden/internal/runtime"
	"github.com/mfat/ezVaultwarden/internal/tailscale"
	"github.com/mfat/ezVaultwarden/internal/vault"
)

// Version is set at release time.
var Version = "dev"

const usage = `ezVaultwarden: run your own Vaultwarden.

  init        Set everything up
  status      Show what's running
  connect     Show how to connect apps and the bw CLI
  expose      Choose who can reach it: tailnet, lan or local
  start       Start the server
  stop        Stop the server
  backup      Make an encrypted backup now
  restore     Restore a backup
  update      Update, with automatic rollback (--check only looks)
  rollback    Go back to the version before the last update
  admin       Open Vaultwarden's admin page until you close it
  logs        Show recent server logs
  uninstall   Remove the service (your vault stays unless you ask)
  version     Show the version
`

const keepBackups = 14

type app struct {
	out   io.Writer
	in    *bufio.Reader
	paths config.Paths
	rt    runtime.Runtime
	now   func() time.Time

	health    func(context.Context, config.State) error
	reachable func(ctx context.Context, url string) error
	ts        tailnet
	issue     func(ctx context.Context, host string) error
	dns       func(token string) dnsHost
	openLink  func(url string)
	lookup    func(host string) ([]string, error)
}

// Run executes one command and returns the process exit code.
func Run(args []string) int {
	paths, err := config.DefaultPaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return 1
	}
	a := &app{
		out:   os.Stdout,
		in:    bufio.NewReader(os.Stdin),
		paths: paths,
		rt:    runtime.Systemd{Paths: paths},
		now:   time.Now,

		reachable: checkReachable,
		ts:        tailscale.New(),
	}
	a.health = a.verify
	a.issue = a.issueCert
	a.dns = func(token string) dnsHost { return cloudflare.New(token) }
	a.openLink = openBrowser
	a.lookup = net.LookupHost
	if len(args) == 0 {
		fmt.Fprint(a.out, usage)
		return 0
	}
	commands := map[string]func([]string) error{
		"init":      a.init,
		"status":    a.status,
		"connect":   a.connect,
		"expose":    a.expose,
		"start":     a.start,
		"stop":      a.stop,
		"backup":    a.backup,
		"restore":   a.restore,
		"update":    a.update,
		"rollback":  a.rollback,
		"admin":     a.admin,
		"logs":      a.logs,
		"uninstall": a.uninstall,

		"renew-cert": a.renewCert, // run by the timer
		"version": func([]string) error {
			fmt.Fprintln(a.out, "ezVaultwarden", Version)
			return nil
		},
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "✗ Unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err := cmd(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "✗", err)
		return 1
	}
	return 0
}

func (a *app) ok(format string, args ...any) {
	fmt.Fprintf(a.out, "✓ "+format+"\n", args...)
}

func (a *app) warn(format string, args ...any) {
	fmt.Fprintf(a.out, "⚠ "+format+"\n", args...)
}

func (a *app) line() (string, error) {
	s, err := a.in.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// confirm asks a yes/no question and takes the default on Enter.
func (a *app) confirm(question string, def bool) (bool, error) {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Fprintf(a.out, "%s %s ", question, hint)
	s, err := a.line()
	if err != nil {
		return false, err
	}
	if s == "" {
		return def, nil
	}
	return strings.EqualFold(s, "y") || strings.EqualFold(s, "yes"), nil
}

func (a *app) secret(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(a.out, prompt)
		return a.line()
	}
	fmt.Fprint(a.out, prompt)
	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return "", err
	}
	defer term.Restore(int(os.Stdin.Fd()), old)
	secret, err := readMasked(os.Stdin, a.out)
	return strings.TrimSpace(secret), err
}

// readMasked echoes a * for each character, so a paste visibly worked. The
// terminal is in raw mode, so it handles Enter, Backspace and Ctrl-C itself.
func readMasked(r io.Reader, w io.Writer) (string, error) {
	var secret []byte
	buf := make([]byte, 256)
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			switch {
			case b == '\r' || b == '\n':
				fmt.Fprint(w, "\r\n")
				return string(secret), nil
			case b == 3:
				fmt.Fprint(w, "\r\n")
				return "", errors.New("cancelled, nothing changed")
			case b == 127 || b == 8:
				if len(secret) > 0 {
					secret = secret[:len(secret)-1]
					fmt.Fprint(w, "\b \b")
				}
			case b >= 32:
				secret = append(secret, b)
				fmt.Fprint(w, "*")
			}
		}
		if err != nil {
			fmt.Fprint(w, "\r\n")
			if errors.Is(err, io.EOF) {
				return string(secret), nil
			}
			return "", err
		}
	}
}

func (a *app) load() (config.State, error) { return config.Load(a.paths) }

func (a *app) start([]string) error {
	if _, err := a.load(); err != nil {
		return err
	}
	if err := a.rt.Start(); err != nil {
		return err
	}
	a.ok("Started")
	return nil
}

func (a *app) stop([]string) error {
	if _, err := a.load(); err != nil {
		return err
	}
	if err := a.rt.Stop(); err != nil {
		return err
	}
	a.ok("Stopped. Start it again with: ezvaultwarden start")
	return nil
}

func (a *app) logs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	n := fs.Int("n", 50, "lines to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := a.load(); err != nil {
		return err
	}
	return a.rt.Logs(a.out, *n)
}

type statusReport struct {
	Running       bool     `json:"running"`
	URL           string   `json:"url"`
	Mode          string   `json:"mode"`
	Version       string   `json:"version"`
	Accounts      []string `json:"accounts"`
	SignupsClosed bool     `json:"signups_closed"`
	Backups       int      `json:"backups"`
	LastBackup    string   `json:"last_backup,omitempty"`
	BackupTargets []string `json:"backup_targets,omitempty"`
}

func (a *app) report(s config.State) statusReport {
	r := statusReport{
		Running: a.rt.Running(), URL: s.URL(), Mode: s.Mode, Version: s.Version,
		SignupsClosed: s.SignupsClosed, BackupTargets: s.BackupTargets,
	}
	r.Accounts, _ = vault.Accounts(a.paths.Data())
	if files, err := backup.List(a.paths.Backups()); err == nil && len(files) > 0 {
		r.Backups = len(files)
		if info, err := os.Stat(files[0]); err == nil {
			r.LastBackup = info.ModTime().Format("2006-01-02 15:04")
		}
	}
	return r
}

func (a *app) status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := a.load()
	if err != nil {
		return err
	}
	r := a.report(s)
	if *asJSON {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	state := "● Running"
	if !r.Running {
		state = "○ Stopped"
	}
	fmt.Fprintf(a.out, "%s  %s  (%s only)\n", state, r.URL, r.Mode)
	fmt.Fprintf(a.out, "  Version   Vaultwarden %s\n", r.Version)
	if r.Backups == 0 {
		fmt.Fprintln(a.out, "  Backups   None yet, the first runs tonight")
	} else {
		where := "⚠ only on this disk"
		if len(r.BackupTargets) > 0 {
			where = "also copied to " + strings.Join(r.BackupTargets, ", ")
		}
		fmt.Fprintf(a.out, "  Backups   Last %s, %d kept, %s\n", r.LastBackup, r.Backups, where)
	}
	overrides := a.overrides(s)
	signups := "signups closed"
	if !r.SignupsClosed {
		signups = "⚠ signups OPEN, finish with: ezvaultwarden init"
	} else if a.signupsReopened() {
		signups = "⚠ signups OPEN"
	}
	fmt.Fprintf(a.out, "  Accounts  %d, %s\n", len(r.Accounts), signups)
	if a.adminLeftOpen() {
		a.warn("The admin page is open. Close it with: ezvaultwarden admin --close")
	}
	if s.Mode == "tailnet" {
		if target, err := a.ts.Proxying(context.Background(), s.Hostname); err != nil || target == "" {
			a.warn("Tailscale isn't serving the vault. Fix it with: ezvaultwarden expose tailnet")
		}
	}
	if problem := a.certProblem(s); problem != "" {
		a.warn("%s", problem)
	}
	if s.Mode == "lan" && !a.nameWorks(s.Hostname) {
		a.warnName(s.Hostname)
	}
	for _, problem := range overrides {
		a.warn("%s", problem)
	}
	return nil
}
