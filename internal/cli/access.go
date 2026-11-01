package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"

	"github.com/mfat/ezVaultwarden/internal/config"
	"github.com/mfat/ezVaultwarden/internal/vault"
)

const homeMenu = `
How do you want to reach your vault?

  1) Only on this computer
     The most private choice. Nothing else can connect, not even your phone.
     Your browser will show a security warning, because the certificate
     is one only this computer knows about.
     Good for: trying it out.

  2) On my home or office network
     Phones and computers on the same Wi-Fi can connect, with no warnings.
     It won't work when you're away, and some routers block it.
     You need: your own domain on Cloudflare. We'll guide you through it.
     Good for: devices that can't use Tailscale.

  3) On all my devices, anywhere (recommended)
     Works at home and away, with no warnings. It stays off the internet,
     so only devices you've added can reach it.
     You need: a free Tailscale account, and the app on each device.
     Good for: most people.

Choose 1, 2 or 3 [3]: `

const serverMenu = `
This machine has its own public address, like a remote server.
How do you want to reach your vault?

  1) Only through an SSH tunnel
     For people comfortable with SSH. Nothing else can connect.

  2) On all my devices, anywhere (recommended)
     Works everywhere, with no warnings, and your vault stays off the internet.
     You need: a free Tailscale account, and the app on each device.
     Good for: most people.

  3) From anywhere, using my own domain
     Open your vault from any device with a normal web address, no extra app
     needed. Your login page will be on the public internet, so anyone can try
     to log in. Your passwords stay encrypted, but a weak master password
     becomes a real risk.
     You need: your own domain on Cloudflare, two-step login on your account,
     and a backup stored somewhere else. You'll create your account through an
     SSH tunnel first, then turn this on.
     Good for: people who can't or won't use Tailscale.

Choose 1, 2 or 3 [2]: `

// chooseAccess comes before the account, because a machine with no screen can't
// open localhost to create one. The second result means the user picked public
// access, which waits until the account is protected. A failed choice returns to
// the menu, since the vault itself is fine.
func (a *app) chooseAccess(ctx context.Context, s config.State, f initFlags) (config.State, bool, error) {
	if f.assume || s.Mode != "local" {
		return s, false, nil
	}
	if emails, _ := vault.Accounts(a.paths.Data()); len(emails) > 0 {
		return s, false, nil
	}
	server := isPublic(a.localIP())
	for {
		choice, planPublic, err := a.pickAccess(server)
		if err != nil {
			return s, false, err
		}
		if choice == "local" {
			a.showTunnel(s)
			return s, planPublic, nil
		}
		if err := a.applyAccess(ctx, s, choice); err != nil {
			a.warn("%v", err)
			fmt.Fprintln(a.out, "  Your vault is fine. Choose again, or fix this and pick the same option.")
			continue
		}
		s, err = a.load()
		return s, false, err
	}
}

func (a *app) applyAccess(ctx context.Context, s config.State, choice string) error {
	if choice == "tailnet" {
		return a.exposeTailnet(ctx, s, true)
	}
	fmt.Fprint(a.out, "Your name for the vault, such as vault.example.com: ")
	host, err := a.line()
	if err != nil {
		return err
	}
	return a.exposeDomain(ctx, s, "lan", []string{"--domain", host}, true)
}

// pickAccess returns the mode to set up now.
func (a *app) pickAccess(server bool) (string, bool, error) {
	menu, choices := homeMenu, map[string]string{"": "tailnet", "1": "local", "2": "lan", "3": "tailnet"}
	if server {
		menu, choices = serverMenu, map[string]string{"": "tailnet", "1": "local", "2": "tailnet", "3": "public"}
	}
	for {
		fmt.Fprint(a.out, menu)
		answer, err := a.line()
		if errors.Is(err, io.EOF) {
			return "", false, errors.New("setup needs an answer. For a script, use: ezvaultwarden init --yes")
		}
		if err != nil {
			return "", false, err
		}
		if choice, ok := choices[answer]; ok {
			if choice == "public" {
				return "local", true, nil
			}
			return choice, false, nil
		}
		fmt.Fprintln(a.out, "Please type one of the numbers.")
	}
}

// showTunnel helps a machine with no desktop, whose localhost nobody can open.
func (a *app) showTunnel(s config.State) {
	if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		return
	}
	name := "you"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	fmt.Fprintln(a.out, "\nThis machine has no browser. On your own computer, open a tunnel to it:")
	fmt.Fprintf(a.out, "  ssh -L %d:localhost:%d %s@%s\n", s.Port, s.Port, name, a.localIP())
	fmt.Fprintln(a.out, "Keep that running, and use the address below in your browser.")
}

func (a *app) showPublicSteps() {
	fmt.Fprintln(a.out, "\nYour vault isn't public yet. Three steps first:")
	fmt.Fprintln(a.out, "  1. Turn on two-step login, in your vault's Security settings")
	fmt.Fprintln(a.out, "  2. Keep backups somewhere else: ezvaultwarden backup --target /path")
	fmt.Fprintln(a.out, "  3. Go public: ezvaultwarden expose public --domain vault.example.com")
}
