# Architecture

ezVaultwarden is a small Go CLI that installs and looks after other people's
software. It doesn't reimplement any of it: Vaultwarden is the password server and
Caddy handles TLS. What we own is setup, safety, backups and updates.

## The pieces

```text
 browser / app / bw CLI
        │  https://localhost:8443
        ▼
 Caddy (127.0.0.1:8443)          TLS, using its own local CA
        │  http
        ▼
 Vaultwarden (127.0.0.1:18443)   Serves the API and the web vault
        │
        ▼
 data/                           The vault
```

Both services bind to loopback. Caddy is the only way in, so future access modes
change what Caddy listens on and nothing else.

## Code layout

| Package | Job |
|---|---|
| `cmd/ezvaultwarden` | Entry point |
| `internal/cli` | Commands and everything the user sees |
| `internal/config` | Folder layout, `state.json`, generated config files |
| `internal/runtime` | Starting and stopping services, behind an interface |
| `internal/fetch` | Verified downloads and self-update |
| `internal/backup` | Encrypted archives |
| `internal/vault` | Reads the account list. Never touches vault contents |

`internal/runtime.Runtime` is the seam for Docker and Podman. Only `systemd.go`
knows how services run, so backups, updates and safety checks stay shared.

## On disk

Everything lives in `~/.local/share/ezvaultwarden`, or `$EZVAULTWARDEN_HOME`.

| Path | What it is |
|---|---|
| `state.json` | Choices made at setup, and what rollback returns to |
| `vaultwarden.env`, `Caddyfile` | Generated from the state. Owner-only |
| `current` | Symlink to the version in use |
| `versions/<id>/` | `vaultwarden`, `web-vault/` and `caddy` for one version |
| `data/` | The vault |
| `backups/` | Encrypted backups, newest 14 |
| `caddy/` | Caddy's storage, including the local CA |
| `data.before-update` | Plain copy for rollback, replaced each update |
| `data.before-restore-*`, `data.replaced-*` | Plain copies. Not yet cleaned up |
| `backup-key.pending` | Only until the user confirms saving the key |
| `linger-enabled` | Marks that we turned on lingering, so `uninstall` can undo it |

Units live in `~/.config/systemd/user`: `ezvaultwarden-server`,
`ezvaultwarden-proxy`, and the `ezvaultwarden-backup` service and timer.

## Downloads

Versions are pinned in `internal/fetch/pins.go`, and each release pins exact content.

- **Vaultwarden and the web vault** come from the official `-alpine` image, pulled
  over the registry API with no container runtime. The index, manifests and layers
  are all checked against digests, and only `vaultwarden` and `web-vault/` are
  written out.
- **Caddy** comes from its GitHub release, checked against a pinned SHA-512.

Files download to an unlinked temp file, so a partial or tampered download can't be
found later. A dropped connection resumes, and only attempts that deliver nothing
count as failures. A folder under `versions/` appears by atomic rename once
complete, so finding one is proof it was verified.

## Setup flow

`init` checks ports and systemd, downloads, writes config, installs the services,
and waits until Caddy answers over TLS with a certificate from our own CA. It then
waits for the first account by reading the database, closes signups, restarts, and
shows the recovery kit. If interrupted, rerunning it resumes.

## Backups

A backup is `tar | gzip | age` of the whole `data/` folder, encrypted to a public
key. `init` generates the key and the user keeps the secret half, so a stolen server
can't read its backups and the daily timer needs no secret. The server stops
briefly so the database is at rest, and comes back even if the backup fails.

`restore` unpacks to a staging folder, checks it contains a database, and only then
swaps it in, keeping what it replaced.

Logged-in clients keep showing their cached copy after a restore or rollback,
because the restored server looks older than what they hold. Both commands say so.

## Updates

Component versions are compiled in, so `update` has two stages:

1. Check `VERSION` in the latest release. If it's newer, download the new program,
   verify it against `checksums.txt`, rename it into place and `exec` it.
2. The new program, which knows the new versions, runs `update --apply`:

```text
download and verify
stop services
copy data/ to data.before-update
encrypted backup
save "previous" to state.json   <- an interrupted update can still be undone
switch the current symlink
start services
check health (binary version, then /alive through Caddy)
  healthy   -> done, prune versions except current and previous
  unhealthy -> old binaries AND old data back, restart, report
```

The binaries never go back alone, because a newer server may have migrated the
database. Data replaced by a rollback goes to `data.replaced-*`.

## Admin page

Vaultwarden has no admin page unless `ADMIN_TOKEN` is set, so `admin` sets one only
while it's needed: a one-time password, stored as an Argon2id hash, removed when the
user presses Enter. A session that gets killed leaves the token behind, which
`status` reports and `admin --close` fixes.

Saving in the admin page writes `data/config.json`, and those values beat the
environment. So `status` reads that file and warns if signups, the domain or a saved
token have changed underneath us.
