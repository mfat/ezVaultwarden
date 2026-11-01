# ezVaultwarden

One command to run your own [Vaultwarden](https://github.com/dani-garcia/vaultwarden).

> **Early development.** Not ready for your real passwords yet. ezVaultwarden is an
> independent project, not affiliated with Vaultwarden or Bitwarden.

```text
$ ezvaultwarden init
✓ Vaultwarden, web vault and Caddy downloaded and verified
✓ Service started at https://localhost:8443

Open https://localhost:8443 and create your account.
✓ Account created
✓ Signups closed, nobody else can register
```

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/mfat/ezVaultwarden/main/install.sh | sh
```

It installs one binary to `~/.local/bin` without root, after checking it against
the release's checksums. Rather not pipe to a shell? Download the binary and
`checksums.txt` from [Releases](https://github.com/mfat/ezVaultwarden/releases),
or read [install.sh](install.sh) first.

Then run `ezvaultwarden init`.

## Why

- **No containers needed.** On Linux it runs the official static binaries as
  systemd user services. No Docker, no daemon, no root.
- **Safe by default.** It listens on this machine only, and signups close by
  themselves as soon as your account exists.
- **Backups from day one.** Daily encrypted backups, attachments included. They're
  encrypted to a public key, so the server can't read its own backups.
- **No lock-in.** Everything is plain config files in one folder.

## Status

| Feature | State |
|---|---|
| Linux native, `local` mode | In progress |
| Encrypted backup and restore | In progress |
| `update` with automatic rollback | Working, needs real-world use |
| Temporary admin page | Working, needs real-world use |
| Docker and Podman | Planned |
| `tailnet` mode | In progress |
| `lan` mode, with Let's Encrypt over Cloudflare DNS | In progress |
| `public` mode with Let's Encrypt | Planned |
| Local web dashboard | Planned |

## Commands

| Command | What it does |
|---|---|
| `init` | Set everything up |
| `status` | Show what's running (`--json` for scripts) |
| `connect` | Show how to connect apps and the `bw` CLI |
| `start`, `stop` | Start or stop the server |
| `backup` | Make a backup now (`--target DIR` keeps copies elsewhere too) |
| `restore` | Restore a backup, keeping a safety copy of the current vault |
| `update` | Update ezVaultwarden, Vaultwarden and Caddy, rolling back by itself if the result isn't healthy (`--check` only looks) |
| `rollback` | Go back to the version and data from before the last update |
| `admin` | Open Vaultwarden's admin page until you close it |
| `logs` | Show recent server logs |
| `uninstall` | Remove the service. Your vault stays unless you pass `--delete-data` |

## Updates

`update` replaces ezVaultwarden, then upgrades Vaultwarden and Caddy to the
versions that release was tested with. It takes an encrypted backup and a local
copy of your data first. If the new version doesn't come up healthy, it returns
to the old binaries and that data by itself.

The binaries can't go back alone: a new Vaultwarden may upgrade the database in a
way the old one can't read. So `rollback` also returns your data to the moment
before the update. Changes made since then are set aside in a `data.replaced-*`
folder, not kept in the vault.

The local copy, `data.before-update`, stays until the next update. It's as
sensitive as the `data` folder next to it, and is protected the same way.

Keep up with updates, because apps move fast. The `bw` CLI 2026.10.1 can't log in
to Vaultwarden 1.37.3, for example, though it works with 1.37.4.

Coming from v0.1.0? It has no `update` command, so rerun the install line once.

## Admin page

Vaultwarden's admin page stays off, because a page that doesn't exist can't be
attacked. `ezvaultwarden admin` opens it with a one-time password, stored only as
an Argon2 hash, and closes it when you press Enter.

Settings saved there go into Vaultwarden's `config.json`, which beats
ezVaultwarden's own. Saving there can reopen signups, so `status` warns you if
that has happened.

## After a restore

Log out and back in on every app. A restored vault looks older to the server
than what your apps already hold, so they keep showing their cached copy until
they log in again.

## Your master password

Nobody can recover it, including this tool. That's how end-to-end encryption
works: the server never has it, and backups contain the same encrypted data.
`init` shows a recovery kit once. Keep it somewhere safe.

## How it works

`init` pulls the Vaultwarden binary and web vault from the official image, and
Caddy from its GitHub release. Each is pinned to an exact digest in
[pins.go](internal/fetch/pins.go) and verified before use. Vaultwarden's project
documents extracting its static binaries this way.

Vaultwarden listens on loopback only. Caddy handles TLS in front of it, using
its own local certificate authority in `local` mode.

## Docs

- [Architecture](docs/architecture.md): how it works
- [Decisions](docs/decisions.md): why it's built this way
- [Roadmap](docs/roadmap.md): what's proven, what's next
- [User experience](docs/ux.md): how it should feel to use
- [End-to-end tests](test/e2e/README.md)

## Building

```bash
go build ./cmd/ezvaultwarden
go test ./...
```

## License

MIT. Vaultwarden and Caddy are separate projects under their own licenses, and
are downloaded from their official sources at install time.
