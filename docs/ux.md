# User experience

The experience we're aiming for. Sections marked *planned* don't exist yet, and the
real output may differ slightly from these examples.

## Rules

- **Safe defaults.** Only `public` mode can be blocked by a safety check, and only
  `--accept-risk` overrides it.
- **Plain language.** Every error says what happened and what to do next.
- **Scriptable.** `--yes` and `--json` for tools.
- **No lock-in.** Everything is normal config files in one folder.
- **Nothing silent.** If something important changed, `status` says so.

## Install and first run

```text
$ curl -fsSL https://raw.githubusercontent.com/mfat/ezVaultwarden/main/install.sh | sh
$ ezvaultwarden init

✓ linux/amd64, running natively
✓ Vaultwarden 1.37.4, web vault and Caddy 2.11.7 downloaded and verified
✓ Service started at https://localhost:8443

Open https://localhost:8443 and create your account.
Your browser will warn you until you import ~/.local/share/ezvaultwarden/caddy/pki/authorities/local/root.crt into its certificate settings.
Waiting for your account...
✓ Account created: mehdi@example.com
✓ Signups closed, nobody else can register
```

Signups closing by itself is the heart of it: no window for a stranger to register,
and no setting to remember.

Then the recovery kit, shown once:

```text
  Server          https://localhost:8443
  Account         mehdi@example.com
  Master password ______________________________
  Backup key      AGE-SECRET-KEY-1...

Nobody can recover your master password, including this tool.
Without the backup key, your backups can't be restored.

Saved it? [y/N]
```

The original design showed a word passphrase. It became a public-key backup key, so
the server can make backups it can't read.

## Connecting clients

`connect` gives exact steps instead of a link to a wiki:

```text
Server URL: https://localhost:8443

Browser extension or desktop app
  Settings > Self-hosted > paste the URL above

bw CLI
  export NODE_EXTRA_CA_CERTS=.../root.crt
  bw config server https://localhost:8443
  bw login mehdi@example.com
```

## Everyday use

Most days you never touch it. `status` answers everything else:

```text
● Running  https://localhost:8443  (local only)
  Version   Vaultwarden 1.37.4
  Backups   Last 2026-11-01 03:00, 14 kept, ⚠ only on this disk
  Accounts  1, signups closed
```

## Updates

```text
$ ezvaultwarden update
ezVaultwarden v0.2.0 → v0.3.0
Stops the service for about 20 seconds. Continue? [Y/n] y

✓ ezVaultwarden updated to v0.3.0
✓ Downloaded and verified
✓ Backup taken
✓ Updated and healthy: Vaultwarden 1.38.0
```

When it goes wrong, it says what happened with no recovery work for you:

```text
✗ Health check failed on 1.38.0
✓ Rolled back to 1.37.4, data restored from before the update
✗ the update failed, and your vault is running as it was. Details: ezvaultwarden logs
```

## Restore

```text
$ ezvaultwarden restore
Choose a backup:
  1) vault-20261101-030000.tar.gz.age  4096 KB
Choice: 1
Backup key: ****
This replaces your current vault. A safety copy is kept. Continue? [y/N] y
✓ Restored and running. The previous vault is in .../data.before-restore-...
⚠ Apps that are logged in still show their old copy. Log out and back in on each one.
```

## Admin page

```text
$ ezvaultwarden admin
Admin page open at https://localhost:8443/admin
Password, for this session only: ...

⚠ Settings you save there override ezVaultwarden's, including closed signups.

Press Enter here to close it.
```

## Removal

`uninstall` stops everything and removes the services. It only deletes vault data
with `--delete-data`, after you type `delete my vault`.

## Going remote *(planned)*

Every step up in exposure is a deliberate choice, and tells you what changes.

```text
$ ezvaultwarden expose tailnet
✓ Available to your devices at https://vault.tail1234.ts.net
  Not reachable from the public internet.

$ ezvaultwarden expose public --domain vault.example.com

This makes your vault login page reachable by anyone.

  ✓ Domain points to this machine
  ✓ Ports 80 and 443 reachable
  ✗ 2FA is not enabled on your account
  ✗ No backup outside this disk

Fix these first, or re-run with --accept-risk.
```

## Real certificates

```text
$ ezvaultwarden expose lan --domain vault.example.com --dns cloudflare
This makes your vault login page reachable from every device on your network,
at https://vault.example.com:8443. Nothing is opened to the internet by this.
The name vault.example.com goes into public certificate logs, and public DNS
will show this machine's local address.
Continuing accepts Let's Encrypt's terms: https://letsencrypt.org/repository/
Continue? [y/N] y
ezVaultwarden needs a Cloudflare API token. On the page that opens, click
Continue to summary, then Create Token. If this account has other domains, first
set Zone Resources to the one that holds vault.example.com. Paste the token here:
  https://dash.cloudflare.com/profile/api-tokens?...
Cloudflare API token:
✓ Certificate issued for vault.example.com, renews automatically
✓ Available on your network at https://vault.example.com:8443
✓ vault.example.com points to this machine, 192.168.1.20
```

## What can't be fully smooth

- **Phones and private certificates.** Phones won't trust the local CA without manual
  setup, so steer them to `tailnet` or a real certificate.
- **The browser's certificate warning.** We never touch the system's certificate
  store, so nothing needs `sudo`. The warning stays until the user imports the
  certificate into their browser.
- **Tailscale setup.** `expose tailnet` needs MagicDNS and HTTPS turned on in the
  Tailscale admin console, plus one `sudo tailscale set --operator=$USER`. It names
  each step when it's missing. The machine's name ends up in public certificate logs.
- **Firewalls and routers for `lan`.** The user may need to allow the port. Some routers
  also block public DNS answers holding a private address, as rebinding protection.
- **The Cloudflare token.** Creating one takes a logged-in person. See decisions.
- **`curl | sh`.** Convenient, but some people won't like it, so manual download
  stays documented.
- **WSL.** Windows browsers need the certificate imported by hand, and WSL can stop
  when idle.
- **A lost master password.** Nothing can fix this, so we work on prevention.
