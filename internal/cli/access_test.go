package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mfat/ezVaultwarden/internal/config"
)

func signupsOpen(t *testing.T, h *harness) config.State {
	t.Helper()
	s, err := h.load()
	if err != nil {
		t.Fatal(err)
	}
	s.SignupsClosed = false
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
	return s
}

func onServer(h *harness) { h.localIP = func() string { return "203.0.113.7" } }

func TestEnterPicksTailnetEverywhere(t *testing.T) {
	for _, server := range []bool{false, true} {
		h, ts, _ := lanSetup(t, "\n")
		if server {
			onServer(h)
		}
		s, plan, err := h.chooseAccess(context.Background(), signupsOpen(t, h), initFlags{})
		if err != nil || plan || s.Mode != "tailnet" || ts.served == "" {
			t.Fatalf("server %v: %+v, %v, %v", server, s, plan, err)
		}
	}
}

func TestHomeMenuOffersTheNetworkAndNotThePublicInternet(t *testing.T) {
	h, _, _ := lanSetup(t, "1\n")
	if _, _, err := h.chooseAccess(context.Background(), signupsOpen(t, h), initFlags{}); err != nil {
		t.Fatal(err)
	}
	out := h.output.String()
	if !strings.Contains(out, "home or office network") || strings.Contains(out, "public internet") {
		t.Fatalf("menu: %s", out)
	}
}

func TestServerMenuHidesTheNetworkOption(t *testing.T) {
	h, _, _ := lanSetup(t, "1\n")
	onServer(h)
	if _, _, err := h.chooseAccess(context.Background(), signupsOpen(t, h), initFlags{}); err != nil {
		t.Fatal(err)
	}
	out := h.output.String()
	if strings.Contains(out, "home or office network") || !strings.Contains(out, "public internet") {
		t.Fatalf("menu: %s", out)
	}
}

func TestPublicChoiceWaitsForTheAccount(t *testing.T) {
	h, _, issued := lanSetup(t, "3\n")
	onServer(h)
	s, plan, err := h.chooseAccess(context.Background(), signupsOpen(t, h), initFlags{})
	if err != nil || !plan || s.Mode != "local" || *issued != 0 {
		t.Fatalf("%+v, plan %v, issued %d, %v", s, plan, *issued, err)
	}
}

func TestHomeNetworkChoiceWorksBeforeTheAccount(t *testing.T) {
	h, _, issued := lanSetup(t, "2\nvault.example.com\ny\ntoken\n")
	s, _, err := h.chooseAccess(context.Background(), signupsOpen(t, h), initFlags{})
	if err != nil || s.Mode != "lan" || *issued != 1 {
		t.Fatalf("%+v, issued %d, %v", s, *issued, err)
	}
	if !strings.Contains(h.output.String(), "register first") {
		t.Fatalf("no warning about the open window: %s", h.output)
	}
}

func TestAFailedChoiceReturnsToTheMenu(t *testing.T) {
	h, ts, _ := lanSetup(t, "3\n1\n")
	ts.lookupErr = errors.New("Tailscale isn't installed")
	s, _, err := h.chooseAccess(context.Background(), signupsOpen(t, h), initFlags{})
	if err != nil || s.Mode != "local" {
		t.Fatalf("%+v, %v", s, err)
	}
	if !strings.Contains(h.output.String(), "isn't installed") {
		t.Fatalf("hid the reason: %s", h.output)
	}
}

func TestABadAnswerAsksAgain(t *testing.T) {
	h, _, _ := lanSetup(t, "9\n1\n")
	if _, _, err := h.chooseAccess(context.Background(), signupsOpen(t, h), initFlags{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "one of the numbers") {
		t.Fatalf("output: %s", h.output)
	}
}

func TestScriptsSkipTheMenu(t *testing.T) {
	h, _, _ := lanSetup(t, "")
	s, _, err := h.chooseAccess(context.Background(), signupsOpen(t, h), initFlags{assume: true})
	if err != nil || s.Mode != "local" || strings.Contains(h.output.String(), "How do you want") {
		t.Fatalf("%+v, %v: %s", s, err, h.output)
	}
}

func TestExposeTailnetStillRefusesOpenSignupsAfterSetup(t *testing.T) {
	h, _, _ := lanSetup(t, "")
	signupsOpen(t, h)
	if err := h.expose([]string{"tailnet"}); err == nil {
		t.Fatal("anyone on the tailnet could register")
	}
}

// vaultDB plants the two tables the safety check reads.
func vaultDB(t *testing.T, h *harness, twoFactorType int) {
	t.Helper()
	path := filepath.Join(h.paths.Data(), "db.sqlite3")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		"CREATE TABLE users (uuid TEXT, email TEXT)",
		"CREATE TABLE twofactor (user_uuid TEXT, atype INTEGER, enabled BOOLEAN)",
		"INSERT INTO users VALUES ('u1', 'a@example.com')",
		fmt.Sprintf("INSERT INTO twofactor VALUES ('u1', %d, 1)", twoFactorType),
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func publicArgs() []string { return []string{"public", "--domain", domain} }

func TestPublicNeedsAServer(t *testing.T) {
	h, _, issued := lanSetup(t, "y\ntoken\n")
	if err := h.expose(publicArgs()); err == nil || *issued != 0 {
		t.Fatalf("err %v, issued %d", err, *issued)
	}
}

func TestPublicNeedsTwoStepLoginAndABackupElsewhere(t *testing.T) {
	h, _, issued := lanSetup(t, "y\ntoken\n")
	onServer(h)
	err := h.expose(publicArgs())
	if err == nil || *issued != 0 {
		t.Fatalf("err %v, issued %d", err, *issued)
	}
	out := h.output.String()
	if !strings.Contains(out, "Two-step login is off") || !strings.Contains(out, "only on this server") {
		t.Fatalf("output: %s", out)
	}
}

func TestPublicGoesAheadOnceProtected(t *testing.T) {
	h, _, issued := lanSetup(t, "y\ntoken\n")
	onServer(h)
	vaultDB(t, h, 0)
	s, _ := h.load()
	s.BackupTargets = []string{"/mnt/usb"}
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
	if err := h.expose(publicArgs()); err != nil || *issued != 1 {
		t.Fatalf("err %v, issued %d: %s", err, *issued, h.output)
	}
	s, _ = h.load()
	if s.Mode != "public" || s.URL() != "https://"+domain+":8443" {
		t.Fatalf("state: %+v", s)
	}
	h.output.Reset()
	if err := h.status(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "public internet") {
		t.Fatalf("status hides it: %s", h.output)
	}
}

func TestChallengesDontCountAsTwoStepLogin(t *testing.T) {
	h, _, _ := lanSetup(t, "")
	vaultDB(t, h, 5) // a remembered device, not a second step
	if h.twoStep() {
		t.Fatal("counted a non-login type")
	}
}
