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
| `update` with automatic rollback | Planned |
| Docker and Podman | Planned |
| `lan` and `tailnet` modes | Planned |
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
| `logs` | Show recent server logs |
| `uninstall` | Remove the service. Your vault stays unless you pass `--delete-data` |

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

## Building

```bash
go build ./cmd/ezvaultwarden
go test ./...
```

## License

MIT. Vaultwarden and Caddy are separate projects under their own licenses, and
are downloaded from their official sources at install time.
