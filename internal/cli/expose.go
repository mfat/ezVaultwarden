package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/mfat/ezVaultwarden/internal/config"
)

// tailnet is the part of Tailscale we need.
type tailnet interface {
	Lookup(ctx context.Context) (string, error)
	Proxying(ctx context.Context, host string) (string, error)
	Serve(ctx context.Context, target string) error
	StopServing(ctx context.Context) error
}

func (a *app) expose(args []string) error {
	if len(args) == 0 {
		return errors.New("choose how to reach your vault: ezvaultwarden expose tailnet|public|local")
	}
	s, err := a.load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[0] {
	case "tailnet":
		return a.exposeTailnet(ctx, s, args[1:])
	case "public":
		return a.exposePublic(ctx, s, args[1:])
	case "lan": // hidden for now, so the error below doesn't list it
		return a.exposeLAN(ctx, s, args[1:])
	case "local":
		return a.exposeLocal(ctx, s, args[1:])
	}
	return fmt.Errorf("unknown option %q. Choose tailnet, public or local", args[0])
}

func (a *app) exposeTailnet(ctx context.Context, s config.State, args []string) error {
	fs := flag.NewFlagSet("expose tailnet", flag.ContinueOnError)
	assume := fs.Bool("yes", false, "don't ask for confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !s.SignupsClosed {
		return errors.New("signups are still open, so anyone who reaches this could register. Finish with: ezvaultwarden init")
	}
	if err := a.confirmSwitch(s, "tailnet", *assume); err != nil {
		return err
	}
	host, err := a.startServing(ctx, fmt.Sprintf("http://127.0.0.1:%d", s.BackendPort))
	if err != nil {
		return err
	}
	before := s
	s.Mode, s.Hostname = "tailnet", host
	if err := a.applyMode(ctx, s); err != nil {
		if stopErr := a.ts.StopServing(ctx); stopErr != nil {
			a.warn("Couldn't stop Tailscale serving the vault: %v", stopErr)
		}
		if undoErr := a.applyMode(ctx, before); undoErr != nil {
			a.warn("Couldn't go back to the previous address: %v", undoErr)
		}
		return err
	}
	if before.UsesLego() {
		a.dropDNS(ctx, before)
	}
	if err := a.confirmAddress(ctx, s); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "  Not reachable from the public internet.")
	fmt.Fprintln(a.out, "  Only devices running Tailscale can connect, so install it on each one.")
	fmt.Fprintln(a.out, "  This machine's name is now in public certificate logs.")
	a.warn("Apps set to the old address need the new one. See: ezvaultwarden connect")
	if before.Mode == "public" {
		fmt.Fprintf(a.out, "\nYou can now delete the DNS record for %s at your domain provider.\n", before.Hostname)
		fmt.Fprintf(a.out, "To go back: ezvaultwarden expose public --domain %s\n", before.Hostname)
	}
	return nil
}

// confirmSwitch says what's about to change before anything does. Every mode has
// clients depending on its address, and going public can't be undone for anyone
// who saw it, so it's the one that defaults to no. Repeating the current mode is
// how a broken setup gets repaired, so that doesn't ask.
func (a *app) confirmSwitch(s config.State, target string, assume bool) error {
	if s.Mode == target {
		return nil
	}
	fmt.Fprintln(a.out, "This changes how you reach your vault. Your account, passwords and backups stay.")
	if s.Mode != "local" {
		fmt.Fprintf(a.out, "  %s stops working straight away.\n", s.URL())
	}
	switch target {
	case "tailnet":
		fmt.Fprintln(a.out, "  Only devices running Tailscale will be able to connect.")
	case "public":
		fmt.Fprintln(a.out, "  Your login page will be on the public internet, so anyone can try to log in.")
		fmt.Fprintln(a.out, "  Use a strong master password and two-step login.")
	default:
		fmt.Fprintln(a.out, "  Only this machine will be able to connect, so your phone and other devices will lose access.")
	}
	if assume {
		return nil
	}
	yes, err := a.confirm("Continue?", target != "public")
	if err != nil {
		return err
	}
	if !yes {
		return errors.New("nothing changed")
	}
	return nil
}

func (a *app) exposeLocal(ctx context.Context, s config.State, args []string) error {
	fs := flag.NewFlagSet("expose local", flag.ContinueOnError)
	assume := fs.Bool("yes", false, "don't ask for confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if s.Mode == "local" {
		a.ok("Already local at %s", s.URL())
		return nil
	}
	if err := a.confirmSwitch(s, "local", *assume); err != nil {
		return err
	}
	before := s
	s.Mode, s.Hostname = "local", ""
	if err := a.applyMode(ctx, s); err != nil {
		return err
	}
	a.leaveMode(ctx, before)
	a.ok("Back to this machine only, at %s", s.URL())
	a.warn("Apps set to the old address need the new one. See: ezvaultwarden connect")
	return nil
}

// applyMode restarts both services: DOMAIN must match the address clients use,
// and the mode decides what Caddy listens on.
func (a *app) applyMode(ctx context.Context, s config.State) error {
	if err := config.Write(a.paths, s); err != nil {
		return err
	}
	if err := config.Save(a.paths, s); err != nil {
		return err
	}
	if err := a.rt.RestartVault(); err != nil {
		return err
	}
	if err := a.rt.RestartProxy(); err != nil {
		return err
	}
	return a.health(ctx, s)
}

// checkReachable proves the public certificate works, the way a client would see
// it. The first request can wait on the certificate being issued.
func checkReachable(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 20 * time.Second}
	deadline := time.Now().Add(time.Minute)
	var last error
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/alive", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("health check: %s", resp.Status)
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return last
}
