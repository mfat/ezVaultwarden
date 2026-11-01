# End-to-end tests

These run the real thing: Vaultwarden, Caddy, systemd and the `bw` CLI. `go test`
covers the logic with fakes. These prove it works.

```bash
python3 test/e2e/e2e.py local
python3 test/e2e/e2e.py upgrade
python3 test/e2e/e2e.py all
```

| Suite | Covers |
|---|---|
| `local` | Install, signups closing, `bw`, backup, restore with attachments, crash recovery, the backup timer, `uninstall` |
| `upgrade` | A real upgrade from an older Vaultwarden, `rollback`, a broken update, a tampered release, the admin page |

## When to run them

Not on every change. `go test ./...` takes seconds and CI runs it on every push.

Run these before a release, and after changing install, update, backup or restore
code. They need real services, so they stay out of CI.

## Speed

Nearly all the time is downloading Vaultwarden. The first run fetches both versions
at once into `~/.cache/ezvaultwarden-e2e`, checked against their digests as usual.
Later runs reuse them, because a version folder that exists skips its download.

`--no-cache` downloads everything, as a real install does. Run it before a release,
since a cached run doesn't exercise the download path through `init` and `update`.

## Before you run them

They install real user services, then remove them. **They stop if an ezVaultwarden
install is already on the machine**, because `uninstall` would remove its services.
Use a spare machine or VM if you run one for real.

Needed:

- Linux with a running user systemd session
- Go, Node.js and npm
- Python 3 with the `cryptography` package
- Ports 8443, 18443 and 18099 free

Data lives in a temp folder, and the account and password are generated per run.
`--keep` leaves that folder behind for debugging, including the test backup key.

## What they can't cover

- The web vault in a real browser. `account.py` registers through the same API.
- The interactive prompts, because they run with `--yes`
- Self-update from a real GitHub release, which uses a local stand-in here

## Keeping them working

`OLD_VAULTWARDEN`, `OLD_DIGEST` and `CLIENT_FOR_OLD` in `e2e.py` name the version
to upgrade from. It must be older than the pin in `internal/fetch/pins.go`, with a
`bw` client old enough to log in to it. Current `bw` releases can't log in to some
older servers, which is why there are two client versions.
