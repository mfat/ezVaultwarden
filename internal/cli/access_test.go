package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mfat/ezVaultwarden/internal/config"
)

const myIP, myDomain = "203.0.113.7", "vault.mydomain.com"

// planSetup is a machine where every check passes.
func planSetup(t *testing.T, input string) (*harness, *fakeTailnet, initFlags) {
	t.Helper()
	h, ts, _ := lanSetup(t, input)
	h.localIP = func() string { return myIP }
	h.resources = func() (uint64, uint64) { return 10 << 30, 1 << 30 }
	h.bind443 = func() error { return nil }
	h.publicIP = func(context.Context) (string, error) { return myIP, nil }
	h.resolve = func(context.Context, string) ([]string, error) { return []string{myIP}, nil }
	return h, ts, initFlags{port: 28443}
}

func TestMenuUsesTheAgreedWordingOnEveryMachine(t *testing.T) {
	for _, ip := range []string{myIP, "192.168.1.20"} {
		h, _, f := planSetup(t, "1\n")
		h.localIP = func() string { return ip }
		if _, err := h.choosePlan(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"1. This computer only", "2. Network access via Tailscale", "3. Public internet access", "on port 443"} {
			if !strings.Contains(h.output.String(), want) {
				t.Fatalf("%s: missing %q", ip, want)
			}
		}
	}
}

func TestLocalPlan(t *testing.T) {
	h, ts, f := planSetup(t, "1\n")
	plan, err := h.choosePlan(context.Background(), f)
	if err != nil || plan.mode != "local" || ts.served != "" {
		t.Fatalf("%+v, %v", plan, err)
	}
}

func TestAnEmptyAnswerAsksAgain(t *testing.T) {
	h, _, f := planSetup(t, "\n9\n1\n")
	plan, err := h.choosePlan(context.Background(), f)
	if err != nil || plan.mode != "local" {
		t.Fatalf("%+v, %v", plan, err)
	}
}

func TestTailnetPlanServesBeforeTheInstall(t *testing.T) {
	h, ts, f := planSetup(t, "2\n")
	plan, err := h.choosePlan(context.Background(), f)
	if err != nil || plan.mode != "tailnet" || plan.host != box {
		t.Fatalf("%+v, %v", plan, err)
	}
	if ts.served != "http://127.0.0.1:38443" {
		t.Fatalf("served %q", ts.served)
	}
}

func TestTailnetProblemsCanBeRetriedOrAbandoned(t *testing.T) {
	h, ts, f := planSetup(t, "2\nb\n1\n")
	ts.lookupErr = errors.New("Tailscale isn't installed")
	plan, err := h.choosePlan(context.Background(), f)
	if err != nil || plan.mode != "local" {
		t.Fatalf("%+v, %v", plan, err)
	}
	if !strings.Contains(h.output.String(), "isn't installed") {
		t.Fatalf("hid the reason: %s", h.output)
	}
}

func TestCheckAgainNoticesTheFix(t *testing.T) {
	h, ts, f := planSetup(t, "2\na\n")
	calls := 0
	h.ts = lookupFails{ts, &calls}
	plan, err := h.choosePlan(context.Background(), f)
	if err != nil || plan.mode != "tailnet" || calls != 2 {
		t.Fatalf("%+v, %d, %v", plan, calls, err)
	}
}

type lookupFails struct {
	*fakeTailnet
	calls *int
}

func (l lookupFails) Lookup(context.Context) (string, error) {
	*l.calls++
	if *l.calls == 1 {
		return "", errors.New("not ready")
	}
	return box, nil
}

func TestPublicPlan(t *testing.T) {
	h, _, f := planSetup(t, "3\n"+myDomain+"\ny\n")
	plan, err := h.choosePlan(context.Background(), f)
	if err != nil || plan.mode != "public" || plan.host != myDomain {
		t.Fatalf("%+v, %v", plan, err)
	}
	if !strings.Contains(h.output.String(), "Let's Encrypt's terms") {
		t.Fatalf("no terms: %s", h.output)
	}
}

func TestPublicDomainMustPointHere(t *testing.T) {
	h, _, f := planSetup(t, "3\n"+myDomain+"\na\nb\n1\n")
	h.resolve = func(context.Context, string) ([]string, error) { return []string{"198.51.100.1"}, nil }
	plan, err := h.choosePlan(context.Background(), f)
	if err != nil || plan.mode != "local" {
		t.Fatalf("%+v, %v", plan, err)
	}
	out := h.output.String()
	if !strings.Contains(out, "A  "+myDomain+"  "+myIP) || !strings.Contains(out, "orange cloud") {
		t.Fatalf("no instructions: %s", out)
	}
}

func TestPort443Problems(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"no permission": {syscall.EACCES, "sudo tee"},
		"in use":        {syscall.EADDRINUSE, "using port 443"},
	}
	for name, tc := range cases {
		h, _, f := planSetup(t, "3\n"+myDomain+"\nb\n1\n")
		h.bind443 = func() error { return tc.err }
		if _, err := h.choosePlan(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(h.output.String(), tc.want) {
			t.Fatalf("%s: %s", name, h.output)
		}
	}
}

func TestPrivateAddressWarnsButNeverRefuses(t *testing.T) {
	h, _, f := planSetup(t, "3\n"+myDomain+"\na\ny\n")
	h.localIP = func() string { return "10.0.0.5" }
	plan, err := h.choosePlan(context.Background(), f)
	if err != nil || plan.mode != "public" {
		t.Fatalf("%+v, %v", plan, err)
	}
	if !strings.Contains(h.output.String(), "may not reach it") {
		t.Fatalf("no warning: %s", h.output)
	}
}

func TestPrivateAddressDefaultsToChoosingAgain(t *testing.T) {
	h, _, f := planSetup(t, "3\n"+myDomain+"\n\n1\n")
	h.localIP = func() string { return "10.0.0.5" }
	plan, err := h.choosePlan(context.Background(), f)
	if err != nil || plan.mode != "local" {
		t.Fatalf("%+v, %v", plan, err)
	}
}

func TestBadDomainsNeverReachCaddy(t *testing.T) {
	for _, bad := range []string{"localhost", "x.com\n}", "a b.com", "--x.com"} {
		h, _, f := planSetup(t, "")
		f.access, f.domain, f.assume = "public", bad, true
		if _, err := h.choosePlan(context.Background(), f); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestScriptsSkipTheQuestion(t *testing.T) {
	h, _, f := planSetup(t, "")
	f.assume = true
	plan, err := h.choosePlan(context.Background(), f)
	if err != nil || plan.mode != "local" || strings.Contains(h.output.String(), "Welcome") {
		t.Fatalf("%+v, %v: %s", plan, err, h.output)
	}
}

func TestLowDiskStopsBeforeDownloading(t *testing.T) {
	h, _, f := planSetup(t, "1\n")
	h.resources = func() (uint64, uint64) { return 100 << 20, 1 << 30 }
	if _, err := h.choosePlan(context.Background(), f); err == nil || !strings.Contains(err.Error(), "disk") {
		t.Fatalf("got %v", err)
	}
}

func TestPublicAddressIsThePlainDomain(t *testing.T) {
	s := config.State{Mode: "public", Hostname: myDomain, Port: 8443, BackendPort: 18443}
	if s.URL() != "https://"+myDomain {
		t.Fatalf("url %s", s.URL())
	}
	caddy := config.Caddyfile(config.Paths{Root: "/r"}, s)
	for _, want := range []string{"disable_http_challenge", "bind 127.0.0.1", "https://" + myDomain + " {", "respond @admin 404"} {
		if !strings.Contains(caddy, want) {
			t.Fatalf("missing %q:\n%s", want, caddy)
		}
	}
}

func TestSignupsCommand(t *testing.T) {
	h, _, _ := planSetup(t, "")
	if err := h.signups([]string{"open"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := h.load(); s.SignupsClosed {
		t.Fatal("still closed")
	}
	if !strings.Contains(env(t, h), "SIGNUPS_ALLOWED=true") {
		t.Fatal("server wasn't told")
	}
	if err := h.signups([]string{"close"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env(t, h), "SIGNUPS_ALLOWED=false") {
		t.Fatal("server wasn't told")
	}
	if err := h.signups(nil); err == nil {
		t.Fatal("no argument accepted")
	}
}

func TestExposePublicFromTailnet(t *testing.T) {
	h, ts, _ := planSetup(t, "y\ny\n")
	if err := h.expose([]string{"tailnet", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := h.expose([]string{"public", "--domain", myDomain}); err != nil {
		t.Fatal(err)
	}
	s, _ := h.load()
	if s.Mode != "public" || ts.stopped != 1 {
		t.Fatalf("%+v, stopped %d", s, ts.stopped)
	}
}

func TestPublicFailureGoesBack(t *testing.T) {
	h, _, _ := planSetup(t, "y\ny\n")
	h.reachable = func(context.Context, string) error { return errors.New("no certificate") }
	if err := h.expose([]string{"public", "--domain", myDomain}); err == nil {
		t.Fatal("expected an error")
	}
	if s, _ := h.load(); s.Mode != "local" {
		t.Fatalf("mode %s", s.Mode)
	}
}

func TestLanIsHiddenFromTheHelp(t *testing.T) {
	if strings.Contains(usage, "lan") {
		t.Fatalf("usage mentions lan: %s", usage)
	}
	h, _, _ := planSetup(t, "")
	err := h.expose([]string{"nonsense"})
	if err == nil || strings.Contains(err.Error(), "lan") {
		t.Fatalf("got %v", err)
	}
}

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

func TestStatusKeepsWarningUntilTwoStepLoginIsOn(t *testing.T) {
	for typ, want := range map[int]bool{5: true, 0: false} { // 5 is a remembered device, not a login step
		h, _, _ := planSetup(t, "")
		vaultDB(t, h, typ)
		s, _ := h.load()
		s.Mode, s.Hostname = "public", myDomain
		if err := config.Save(h.paths, s); err != nil {
			t.Fatal(err)
		}
		if err := h.status(nil); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(h.output.String(), "Two-step login is off"); got != want {
			t.Fatalf("type %d: warned %v", typ, got)
		}
	}
}

func TestTunnelHelpOnlyShowsWithoutADesktop(t *testing.T) {
	s := config.State{Mode: "local", Port: 8443}
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")
	h, _, _ := planSetup(t, "")
	h.showAccountStep(s)
	if strings.Contains(h.output.String(), "ssh -L") {
		t.Fatalf("a desktop doesn't need a tunnel: %s", h.output)
	}

	t.Setenv("DISPLAY", "")
	t.Setenv("SSH_CONNECTION", "198.51.100.9 50000 203.0.113.7 22")
	h.output.Reset()
	h.showAccountStep(s)
	if !strings.Contains(h.output.String(), "@203.0.113.7") {
		t.Fatalf("no real address: %s", h.output)
	}

	t.Setenv("SSH_CONNECTION", "")
	if !strings.HasSuffix(sshTarget(), "@this-server") {
		t.Fatalf("target %s", sshTarget())
	}
}

func TestStatusNoticesAProxyTurnedOnLater(t *testing.T) {
	h, _, _ := planSetup(t, "")
	s, _ := h.load()
	s.Mode, s.Hostname = "public", myDomain
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
	if err := h.status(nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.output.String(), "orange cloud") {
		t.Fatalf("warned about a healthy name: %s", h.output)
	}
	h.resolve = func(context.Context, string) ([]string, error) { return []string{"104.21.72.85"}, nil }
	h.output.Reset()
	if err := h.status(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "orange cloud") {
		t.Fatalf("no warning: %s", h.output)
	}
}

func publicVault(t *testing.T, h *harness) {
	t.Helper()
	s, _ := h.load()
	s.Mode, s.Hostname = "public", myDomain
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
}

func TestLeavingPublicExplainsTheSwitch(t *testing.T) {
	h, ts, _ := planSetup(t, "y\n")
	publicVault(t, h)
	if err := h.expose([]string{"tailnet"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := h.load(); s.Mode != "tailnet" || ts.served == "" {
		t.Fatalf("%+v", s)
	}
	for _, want := range []string{"passwords and backups stay", "stops working straight away", "install it on each one",
		"delete the DNS record for " + myDomain, "expose public --domain " + myDomain} {
		if !strings.Contains(h.output.String(), want) {
			t.Fatalf("missing %q: %s", want, h.output)
		}
	}
}

func TestSayingNoToLeavingPublicChangesNothing(t *testing.T) {
	h, ts, _ := planSetup(t, "n\n")
	publicVault(t, h)
	if err := h.expose([]string{"tailnet"}); err == nil {
		t.Fatal("expected a refusal")
	}
	if s, _ := h.load(); s.Mode != "public" || ts.served != "" {
		t.Fatalf("%+v", s)
	}
}

func TestEverySwitchSaysWhatChanges(t *testing.T) {
	cases := []struct {
		from, to string
		args     []string
		want     string
	}{
		{"local", "tailnet", []string{"tailnet"}, "Only devices running Tailscale"},
		{"tailnet", "local", []string{"local"}, "lose access"},
		{"public", "local", []string{"local"}, "stops working straight away"},
		{"tailnet", "public", []string{"public", "--domain", myDomain}, "public internet"},
	}
	for _, tc := range cases {
		h, _, _ := planSetup(t, "n\n")
		s, _ := h.load()
		s.Mode, s.Hostname = tc.from, box
		if err := config.Save(h.paths, s); err != nil {
			t.Fatal(err)
		}
		if err := h.expose(tc.args); err == nil {
			t.Fatalf("%s to %s went ahead without a yes", tc.from, tc.to)
		}
		if !strings.Contains(h.output.String(), tc.want) {
			t.Fatalf("%s to %s: %s", tc.from, tc.to, h.output)
		}
		if s, _ := h.load(); s.Mode != tc.from {
			t.Fatalf("%s to %s changed the mode", tc.from, tc.to)
		}
	}
}

func TestGoingPublicDefaultsToNo(t *testing.T) {
	h, _, _ := planSetup(t, "\n")
	if err := h.expose(publicArgsFor(myDomain)); err == nil {
		t.Fatal("pressing Enter went public")
	}
}

func publicArgsFor(host string) []string { return []string{"public", "--domain", host} }

func TestRepeatingAModeToRepairItDoesntAsk(t *testing.T) {
	h, ts, _ := planSetup(t, "")
	s, _ := h.load()
	s.Mode, s.Hostname = "tailnet", box
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
	if err := h.expose([]string{"tailnet"}); err != nil || ts.served == "" {
		t.Fatalf("%v, served %q", err, ts.served)
	}
}
