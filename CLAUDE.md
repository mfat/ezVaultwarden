# ezVaultwarden

A Go CLI that installs and looks after a personal Vaultwarden server. On Linux it
runs static binaries as systemd user services, with no Docker and no root.

Read these before designing anything:

- [docs/architecture.md](docs/architecture.md): how it works
- [docs/decisions.md](docs/decisions.md): why, and what would change our mind
- [docs/roadmap.md](docs/roadmap.md): what's proven, and what's next
- [docs/ux.md](docs/ux.md): how it should feel to use
- [test/e2e/README.md](test/e2e/README.md): the end-to-end tests

## Current state

v0.3.1 is released. Its setup wizard offers this computer only, Tailscale, or the
public internet with your own domain. The README warns against real passwords, and
that stays until the Linux checks below are done.

It was developed on Windows and tested in WSL. A first run on a real Linux machine
worked: install, a browser signup closing signups, the "Saved it?" prompt, the web
vault in Firefox after accepting the warning, and a browser extension logging in.
That run used v0.2.0, which still had the system certificate step. Later, a clean
Ubuntu 24.04 VPS passed the end-to-end tests, the setup menu and a reboot.

## Next steps, in order

1. **Finish the real Linux checks.** The maintainer skipped these for now, so they're
   still unproven. Use throwaway data:
   - the desktop app logging in
   - importing the certificate by hand, and whether browsers then trust the page
2. **`init --from-backup`.** Restoring on a new machine is untested and undocumented.
3. **Clean up** `data.before-restore-*` and `data.replaced-*`, which are plain copies
   that nothing removes.
4. **Fix what real use finds,** then drop the README warning.
5. **`tailnet` mode.** `expose tailnet` works on a real tailnet.
6. **`public` mode.** Caddy on port 443, from the setup wizard or `expose public`.
7. **`expose lan` is hidden.** It's built with `lego` and Cloudflare, and stays out of the
   menu and help. Keep it working, but don't show it.

Decided but not built: see the roadmap. WSL detection with a Windows certificate
step is a small addition worth making alongside item 2.

## Working here

- Run `go test ./...` for everyday changes. It takes seconds, and CI runs it on push.
- Run `gofmt -l .` and `go vet ./...` before committing.
- Run the end-to-end tests only before a release, or after changing install, update,
  backup or restore code. They're slow the first time and install real services.
- **Don't run the end-to-end tests on a machine with a real install.** They refuse to,
  because `uninstall` would remove its services. Use a VM.
- Before a release, run them with `--no-cache` once.
- The `upgrade` suite is off because it takes too long. Don't run or re-enable it unless
  the maintainer asks.
- A new Vaultwarden needs a new release: update the pins in `internal/fetch/pins.go`.
- Releases come from pushing a `v*` tag. Never mark one a prerelease, because the
  `latest/download` links `install.sh` uses skip those.

## Things that bit us

- `lego` 5 has no `renew` command. `run` does both.
- Routers can drop public DNS answers holding a private address, so a correct record
  can still fail in `lan` mode. `192.168.1.1.nip.io` is a quick test. Asking for a name
  before its record exists also caches NXDOMAIN for up to 30 minutes.
- Some networks intercept all DNS, so `lego`'s own record check fails with NXDOMAIN.
  An answer from `dig @192.0.2.1` proves it. Hence the fixed wait.
- `bw` refuses plain HTTP, even on localhost.
- `bw` keeps its cached vault after a restore or rollback. Test with a fresh login.
- `bw` 2026.10.1 can't log in to Vaultwarden 1.37.3. New clients can break old servers.
- `DOMAIN` must match the client URL, or attachments break.
- `systemctl is-active` with several units succeeds if any one is up.
- Settings saved in the admin page go in `data/config.json`, which beats the
  environment and can reopen signups.
- Vaultwarden's registration API moved to `/identity/accounts/register/finish`.
- `-race` needs cgo, so it runs in CI but not with `CGO_ENABLED=0`.

## Commits

- Never attribute commits to Claude: no `Co-Authored-By` trailers or "Generated with"
  lines.
- Commit or push only when the maintainer asks. Publishing a release needs an
  explicit yes.
- Commit messages: a short sentence ending in a full stop, then why it changed.
- Never put real passwords, backup keys or test credentials in the repo.
