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

v0.2.0 is released. It's reachable from the same machine only, or over an SSH tunnel
(untested). The README warns against real passwords, and that stays until the
Linux checks below are done.

It was developed on Windows, with everything tested in WSL. It has **never run on a
real Linux machine**, so treat that as the first thing to find out.

## Next steps, in order

1. **Check it on real Linux with the maintainer.** Use throwaway data:
   - importing the certificate by hand, and whether browsers then trust the page
   - a real browser signup, with `init` noticing it and closing signups
   - the recovery kit and "Saved it?" prompts, which only `--yes` has exercised
   - a reboot: does the vault come back by itself?
   - a browser extension or desktop app logging in
2. **`init --from-backup`.** Restoring on a new machine is untested and undocumented.
3. **Clean up** `data.before-restore-*` and `data.replaced-*`, which are plain copies
   that nothing removes.
4. **Fix what real use finds,** drop the README warning, release v0.3.0. That's also
   the first real test of self-update, from v0.2.0.
5. **`tailnet` mode.** It should give real certificates with no domain or open ports,
   and it's the priority over Let's Encrypt on a custom domain. Check Tailscale's
   docs first: that its certificates work this way is from memory, not verified.

Decided but not built: see the roadmap. WSL detection with a Windows certificate
step is a small addition worth making alongside item 2.

## Working here

- Run `go test ./...` for everyday changes. It takes seconds, and CI runs it on push.
- Run `gofmt -l .` and `go vet ./...` before committing.
- Run the end-to-end tests only before a release, or after changing install, update,
  backup or restore code. They're slow the first time and install real services.
- **Don't run the end-to-end tests on a machine with a real install.** They refuse to,
  because `uninstall` would remove its services. Use a VM.
- Before a release, run them with `--no-cache` once. The cached ported `upgrade` suite
  and the cache itself have never completed a run, so expect to fix small things.
- A new Vaultwarden needs a new release: update the pins in `internal/fetch/pins.go`,
  and bump `OLD_VAULTWARDEN` in `test/e2e/e2e.py` if needed.
- Releases come from pushing a `v*` tag. Never mark one a prerelease, because the
  `latest/download` links `install.sh` uses skip those.

## Things that bit us

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
