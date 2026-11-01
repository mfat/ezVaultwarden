package cli

import (
	"context"
	"errors"
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
		return errors.New("choose a mode: ezvaultwarden expose tailnet|lan|local")
	}
	s, err := a.load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[0] {
	case "tailnet":
		return a.exposeTailnet(ctx, s)
	case "lan":
		return a.exposeLAN(ctx, s, args[1:])
	case "local":
		return a.exposeLocal(ctx, s)
	}
	return fmt.Errorf("unknown mode %q. Choose tailnet, lan or local", args[0])
}

func (a *app) exposeTailnet(ctx context.Context, s config.State) error {
	if !s.SignupsClosed {
		return errors.New("signups are still open, so anyone who reaches this could register. Finish with: ezvaultwarden init")
	}
	host, err := a.ts.Lookup(ctx)
	if err != nil {
		return err
	}
	target := fmt.Sprintf("http://127.0.0.1:%d", s.BackendPort)
	current, err := a.ts.Proxying(ctx, host)
	if err != nil {
		return err
	}
	if current != "" && current != target {
		return fmt.Errorf("port 443 on %s is already in use by Tailscale (%s). Free it first", host, current)
	}
	if err := a.ts.Serve(ctx, target); err != nil {
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
	if err := a.reachable(ctx, s.URL()); err != nil {
		a.warn("It didn't answer at %s yet: %v", s.URL(), err)
		fmt.Fprintln(a.out, "  The first certificate can take a minute. Try the address in a browser.")
	} else {
		a.ok("Available to your devices at %s", s.URL())
	}
	fmt.Fprintln(a.out, "  Not reachable from the public internet.")
	fmt.Fprintln(a.out, "  This machine's name is now in public certificate logs.")
	a.warn("Apps set to the old address need the new one. See: ezvaultwarden connect")
	return nil
}

func (a *app) exposeLocal(ctx context.Context, s config.State) error {
	if s.Mode == "local" {
		a.ok("Already local at %s", s.URL())
		return nil
	}
	if s.Mode == "tailnet" {
		if err := a.ts.StopServing(ctx); err != nil {
			return err
		}
	}
	s.Mode, s.Hostname = "local", ""
	if err := a.applyMode(ctx, s); err != nil {
		return err
	}
	if err := a.forgetDNSToken(); err != nil {
		return err
	}
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
