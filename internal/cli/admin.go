package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/argon2"

	"github.com/mfat/ezVaultwarden/internal/config"
)

// Vaultwarden's own default cost for hashing an admin token.
const (
	argonMemory = 65540
	argonTime   = 3
	argonThread = 4
)

// hashAdminToken returns the argon2id PHC string Vaultwarden expects, so the
// password itself is never written to disk.
func hashAdminToken(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThread, 32)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		argonMemory, argonTime, argonThread, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// admin opens Vaultwarden's admin page only while it's needed. With no token
// configured the page doesn't exist, which beats any login in front of it.
func (a *app) admin(args []string) error {
	fs := flag.NewFlagSet("admin", flag.ContinueOnError)
	closeOnly := fs.Bool("close", false, "close an admin page left open")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := a.load()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *closeOnly {
		return a.closeAdmin(s)
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	if s.AdminToken, err = hashAdminToken(password); err != nil {
		return err
	}
	if err := a.rewrite(ctx, s); err != nil {
		return errors.Join(err, a.closeAdmin(s))
	}
	if s.Mode == "public" {
		// The public site never serves it, so reach it through a tunnel.
		fmt.Fprintf(a.out, "Admin page open at %s/admin, through: ssh -L %d:localhost:%d you@your-server\n", s.LocalURL(), s.Port, s.Port)
	} else {
		fmt.Fprintf(a.out, "Admin page open at %s/admin\n", s.URL())
	}
	fmt.Fprintf(a.out, "Password, for this session only: %s\n\n", password)
	a.warn("Settings you save there override ezVaultwarden's, including closed signups.")
	fmt.Fprintln(a.out, "\nPress Enter here to close it.")

	done := make(chan struct{})
	go func() {
		_, _ = a.line()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return a.closeAdmin(s)
}

func (a *app) closeAdmin(s config.State) error {
	s.AdminToken = ""
	if err := a.rewrite(context.Background(), s); err != nil {
		return err
	}
	for _, problem := range a.overrides(s) {
		a.warn("%s", problem)
	}
	a.ok("Admin page closed")
	return nil
}

func (a *app) rewrite(ctx context.Context, s config.State) error {
	if err := config.Write(a.paths, s); err != nil {
		return err
	}
	if err := a.rt.RestartVault(); err != nil {
		return err
	}
	return a.health(ctx, s)
}

// adminLeftOpen spots a session that was killed before it could close.
func (a *app) adminLeftOpen() bool {
	env, err := os.ReadFile(a.paths.VaultEnv())
	return err == nil && strings.Contains(string(env), "ADMIN_TOKEN=")
}

type savedSettings struct {
	Signups *bool   `json:"signups_allowed"`
	Domain  *string `json:"domain"`
	Token   *string `json:"admin_token"`
}

func (a *app) signupsReopened() bool {
	raw, err := os.ReadFile(filepath.Join(a.paths.Data(), "config.json"))
	if err != nil {
		return false
	}
	var saved savedSettings
	return json.Unmarshal(raw, &saved) == nil && saved.Signups != nil && *saved.Signups
}

// overrides reports what Vaultwarden's config.json has changed. Saving in the
// admin page writes that file, and its values beat the environment, so a
// setting ezVaultwarden depends on can change without anything here knowing.
func (a *app) overrides(s config.State) []string {
	raw, err := os.ReadFile(filepath.Join(a.paths.Data(), "config.json"))
	if errors.Is(err, fs.ErrNotExist) || err != nil {
		return nil
	}
	var saved savedSettings
	if err := json.Unmarshal(raw, &saved); err != nil {
		return []string{"Vaultwarden's config.json can't be read: " + err.Error()}
	}
	var problems []string
	if saved.Signups != nil && *saved.Signups && s.SignupsClosed {
		problems = append(problems, "Signups are OPEN: the admin page settings override ezVaultwarden. Turn them off there.")
	}
	if saved.Domain != nil && *saved.Domain != s.URL() {
		problems = append(problems, fmt.Sprintf("The admin page set the domain to %s, which can break attachments.", *saved.Domain))
	}
	if saved.Token != nil && *saved.Token != "" {
		problems = append(problems, "The admin page stays open: its settings saved a token. Remove admin_token from "+
			filepath.Join(a.paths.Data(), "config.json")+" and run: ezvaultwarden admin --close")
	}
	return problems
}
