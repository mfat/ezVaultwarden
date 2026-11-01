package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mfat/ezVaultwarden/internal/config"
)

const box = "box.tail1.ts.net"

type fakeTailnet struct {
	lookupErr error
	current   string
	serveErr  error
	served    string
	stopped   int
	stopErr   error
}

func (f *fakeTailnet) Lookup(context.Context) (string, error) { return box, f.lookupErr }
func (f *fakeTailnet) Proxying(context.Context, string) (string, error) {
	return f.current, nil
}
func (f *fakeTailnet) Serve(_ context.Context, target string) error {
	if f.serveErr != nil {
		return f.serveErr
	}
	f.served = target
	return nil
}
func (f *fakeTailnet) StopServing(context.Context) error {
	f.stopped++
	return f.stopErr
}

func exposeSetup(t *testing.T) (*harness, *fakeTailnet) {
	t.Helper()
	h := setup(t, "")
	ts := &fakeTailnet{}
	h.ts = ts
	h.reachable = func(context.Context, string) error { return nil }
	s, err := h.load()
	if err != nil {
		t.Fatal(err)
	}
	s.BackendPort = 18443
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
	return h, ts
}

func env(t *testing.T, h *harness) string {
	t.Helper()
	raw, err := os.ReadFile(h.paths.VaultEnv())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestExposeTailnetMovesTheVaultToItsTailnetName(t *testing.T) {
	h, ts := exposeSetup(t)
	if err := h.expose([]string{"tailnet", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if ts.served != "http://127.0.0.1:18443" {
		t.Fatalf("served %q", ts.served)
	}
	s, _ := h.load()
	if s.Mode != "tailnet" || s.URL() != "https://"+box {
		t.Fatalf("state: %+v", s)
	}
	if !strings.Contains(env(t, h), "DOMAIN=https://"+box+"\n") {
		t.Fatalf("DOMAIN must match the client URL:\n%s", env(t, h))
	}
	if s.LocalURL() != "https://localhost:8443" {
		t.Fatalf("health checks need the local address, got %s", s.LocalURL())
	}
}

func TestExposeRefusesWhileSignupsAreOpen(t *testing.T) {
	h, ts := exposeSetup(t)
	s, _ := h.load()
	s.SignupsClosed = false
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
	if err := h.expose([]string{"tailnet", "--yes"}); err == nil || !strings.Contains(err.Error(), "signups") {
		t.Fatalf("got %v", err)
	}
	if ts.served != "" {
		t.Fatal("exposed a server anyone could register on")
	}
}

func TestExposeLeavesSomeoneElsesServiceAlone(t *testing.T) {
	h, ts := exposeSetup(t)
	ts.current = "http://127.0.0.1:3000"
	if err := h.expose([]string{"tailnet", "--yes"}); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("got %v", err)
	}
	if ts.served != "" {
		t.Fatal("replaced another service")
	}
}

func TestExposeTwiceIsFine(t *testing.T) {
	h, ts := exposeSetup(t)
	if err := h.expose([]string{"tailnet", "--yes"}); err != nil {
		t.Fatal(err)
	}
	ts.current = ts.served
	if err := h.expose([]string{"tailnet", "--yes"}); err != nil {
		t.Fatal(err)
	}
}

func TestFailedServeChangesNothing(t *testing.T) {
	h, ts := exposeSetup(t)
	ts.serveErr = errors.New("denied")
	if err := h.expose([]string{"tailnet", "--yes"}); err == nil {
		t.Fatal("expected an error")
	}
	if s, _ := h.load(); s.Mode != "local" {
		t.Fatalf("mode changed to %s", s.Mode)
	}
}

func TestUnhealthyServerGoesBackToLocal(t *testing.T) {
	h, ts := exposeSetup(t)
	h.health = func(_ context.Context, s config.State) error {
		if s.Mode == "tailnet" {
			return errors.New("down")
		}
		return nil
	}
	if err := h.expose([]string{"tailnet", "--yes"}); err == nil {
		t.Fatal("expected an error")
	}
	s, _ := h.load()
	if s.Mode != "local" || s.Hostname != "" || ts.stopped != 1 {
		t.Fatalf("not undone: %+v, stopped %d", s, ts.stopped)
	}
	if !strings.Contains(env(t, h), "DOMAIN=https://localhost:8443\n") {
		t.Fatalf("DOMAIN not restored:\n%s", env(t, h))
	}
}

func TestExposeLocalUndoesTailnet(t *testing.T) {
	h, ts := exposeSetup(t)
	if err := h.expose([]string{"tailnet", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := h.expose([]string{"local", "--yes"}); err != nil {
		t.Fatal(err)
	}
	s, _ := h.load()
	if s.Mode != "local" || s.Hostname != "" || ts.stopped != 1 {
		t.Fatalf("state: %+v, stopped %d", s, ts.stopped)
	}
}

func TestUninstallStopsServingOverTailscale(t *testing.T) {
	h, ts := exposeSetup(t)
	if err := h.expose([]string{"tailnet", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := h.uninstall(nil); err != nil {
		t.Fatal(err)
	}
	if ts.stopped != 1 {
		t.Fatal("Tailscale still forwards to a server that's gone")
	}
}

func TestStatusNoticesTailscaleNoLongerServes(t *testing.T) {
	h, ts := exposeSetup(t)
	if err := h.expose([]string{"tailnet", "--yes"}); err != nil {
		t.Fatal(err)
	}
	h.output.Reset()
	ts.current = ""
	if err := h.status(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "ezvaultwarden expose tailnet") {
		t.Fatalf("no warning: %s", h.output)
	}
}

func TestConnectDropsLocalCertificateStepsOnATailnet(t *testing.T) {
	h, _ := exposeSetup(t)
	if err := h.expose([]string{"tailnet", "--yes"}); err != nil {
		t.Fatal(err)
	}
	h.output.Reset()
	if err := h.connect(nil); err != nil {
		t.Fatal(err)
	}
	out := h.output.String()
	if strings.Contains(out, "NODE_EXTRA_CA_CERTS") || strings.Contains(out, "warn you") {
		t.Fatalf("a real certificate needs none of this: %s", out)
	}
	if !strings.Contains(out, "https://"+box) {
		t.Fatalf("no address: %s", out)
	}
}
