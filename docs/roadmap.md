# Roadmap

## Where it stands

v0.3.0 runs Vaultwarden on Linux, reachable from that machine only, over Tailscale, or
on the public internet with your own domain, with backups, updates and rollback. It's early: the README warns against real passwords until the
first items below are done.

**Proven end to end, in WSL:** install, signups closing, backup and restore with
attachments, a real upgrade, rollback, a broken update, a tampered release, the admin
page, and clean removal. See [test/e2e](../test/e2e/README.md).

**Proven on a real Linux machine, with v0.2.0:** install, a browser signup closing
signups, the "Saved it?" prompt, the web vault in Firefox after accepting the warning,
and a browser extension logging in.

**Proven on a clean Ubuntu 24.04 VPS:** the end-to-end tests with fresh downloads, the
setup wizard's public path, a real Let's Encrypt certificate over port 443 alone, `signups
close`, a stranger refused once signups closed, `expose local` taking it off the internet,
and the vault returning after a reboot with nobody logged in.

**Not proven yet:**

- Importing the certificate into Linux browsers
- The desktop app logging in
- `expose local` from `tailnet` on a real machine, and a phone's Bitwarden app
- The wizard's Tailscale path on a fresh install, and the menu in a real terminal
- `init` resuming after `signups close` with no account yet
- A public certificate renewing, and clients logging in to a public vault
- `expose lan` reaching the vault from another device. It's hidden from users for now
- Self-update from a real GitHub release. The first test is v0.2.0 to the next one
- The `arm64` build, which has been built but never run

## Next

1. **Finish testing on a real Linux machine.** The desktop app and certificate import
   are skipped for now. This needs a person, and may reorder what follows.
2. **`init --from-backup`.** Restoring on a new machine works around `init` today:
   untested, undocumented, and signups stay open until the last step. Disaster
   recovery is why backups exist.
3. **Clean up old copies.** `data.before-restore-*` and `data.replaced-*` are never
   removed. They're plain copies, so they shouldn't pile up.
4. **Fix what real use finds,** then remove the README warning.

## Then

5. **Better WSL support.** Detect it, and print the Windows certificate step.
6. **`tailnet` mode.** Built as `expose tailnet`, and working on a real tailnet. Phones
   and other devices can reach the vault with no public exposure and a real certificate.
7. **Docker and Podman,** behind the existing `Runtime` interface. This also gives
   Mac and Windows users a path.
8. **`lan` mode,** built as `expose lan`, but hidden from users.
9. **`public` mode,** built with Caddy on port 443.
10. **Local web dashboard.**

## Planned designs

### Access modes

Each is opt-in, and going back is one command: `ezvaultwarden expose local`.

| Mode | Reachable from | Certificate |
|---|---|---|
| `local` (today) | This machine | Caddy's private CA |
| `tailnet` | Your own devices anywhere | Tailscale |
| `public` | The internet | Let's Encrypt, through Caddy on port 443 |
| `lan` (hidden) | Your network | Let's Encrypt by DNS challenge |

Phones won't trust a private CA without manual setup, so point phone users at
`tailnet` or a real certificate.

### Real certificates

| Method | Needs | Best for |
|---|---|---|
| HTTP or TLS challenge | Ports 80 or 443 open | `public` |
| DNS challenge | A DNS API token, no open ports | `lan` and private servers |

- Official Caddy binaries have no DNS provider plugins. Use `lego`, which has
  checksummed Linux releases, and hand its certificates to Caddy. That keeps Caddy
  official and verifiable.
- Binding ports 80 and 443 natively needs one `sudo` step, or a forward.
- Issued certificates appear in public logs. A wildcard hides the hostname.

### `public` mode safety

`status` warns until two-step login is on. It should also warn loudly about pending
security updates, and about login rate limits.

### Update options

- A systemd timer that checks daily and notifies. Installing by itself stays opt-in,
  because an unattended restart of a password server shouldn't be a silent default.
- `status` showing pending updates.

### Dashboard

`ezvaultwarden ui` serves status, backups, updates and exposure from the same binary.
It can restore backups and expose the server, so it's localhost only, opens with a
one-time link, and stops when closed. Remote use goes through an SSH tunnel. It never
sees vault contents.

### Recovery help

- A printable recovery kit.
- Password hints available locally through the CLI, never on the login page.
- `status` suggesting emergency access.
- `reset-account`: a clearly labelled fresh start, not a recovery.

### Test mode

A disposable `--test` install with a throwaway account would let sshPilot's CI run its
Bitwarden tests against a real server.

## Platforms

| Platform | Plan |
|---|---|
| Linux x86-64 and ARM | Native |
| Windows | WSL now, Docker Desktop later. No native plans |
| macOS | Docker or Podman. Check Homebrew's package |
| FreeBSD | Later, through `pkg` |

## Maintenance

- GitHub Actions warns that `actions/checkout@v4` and `actions/setup-go@v5` target
  Node.js 20, and that `ubuntu-latest` is moving to Ubuntu 26.
- `go.mod` requires Go 1.27.3, which may be stricter than contributors need.
- Check the MIT and AGPL position before depending on it. We only download Vaultwarden
  and don't redistribute it.
