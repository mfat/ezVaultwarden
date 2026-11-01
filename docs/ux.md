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

Welcome to ezVaultwarden. This wizard will walk you through setting up your own Vaultwarden instance.

Before install, please tell me how you want to access your vault:

1. This computer only
Vaultwarden will be installed on this computer, and it will NOT be accessible from the internet or your local Network.
Note: Uses a self-signed certificate you should accept in your browser

2. Network access via Tailscale
Vaultwarden will be accessible from the internet only by your own devices running Tailscale.
*Requires free Tailscale account

3. Public internet access
Vaultwarden will be available from the public internet using your own domain name. (for example vault.mydomain.com).
*Requires a server accessible from the internet on port 443 and a domain name

Choose 1, 2 or 3: 2

  Checking this machine...
✓ 11 GB of disk and 960 MB of memory free, ports are ready
✓ Tailscale is connected as mybox.tail1234.ts.net

ezVaultwarden v0.3.0

✓ linux/amd64, running natively
✓ Vaultwarden 1.37.4, web vault and Caddy 2.11.7 downloaded and verified
✓ Service started
✓ Available to your devices at https://mybox.tail1234.ts.net

Open https://mybox.tail1234.ts.net and create your account.
Only your own devices can reach this.
Waiting for your account...
✓ Account created: mehdi@example.com
✓ Signups closed, nobody else can register
```

Signups closing by itself is the heart of it: no setting to remember. On public access
there is a short window before the first account, so the wizard says so plainly.

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

## The full-screen app

`ezvaultwarden ui` opens a home screen showing what's running, with screens for connecting
apps, backups, updates, changing access, logs and uninstalling. A banner says where you
are, and the keys sit along the bottom. Every change asks first, and shows the real
command's output as it runs.

## Changing access later

```text
$ ezvaultwarden expose tailnet
This changes how you reach your vault. Your account, passwords and backups stay.
  Only devices running Tailscale will be able to connect.
Continue? [Y/n] y
✓ Available to your devices at https://vault.tail1234.ts.net
  Not reachable from the public internet.
  Only devices running Tailscale can connect, so install it on each one.
  This machine's name is now in public certificate logs.
⚠ Apps set to the old address need the new one. See: ezvaultwarden connect

$ ezvaultwarden expose public --domain vault.mydomain.com
This changes how you reach your vault. Your account, passwords and backups stay.
  https://vault.tail1234.ts.net stops working straight away.
  Your login page will be on the public internet, so anyone can try to log in.
  Use a strong master password and two-step login.
Continue? [y/N] y
✓ Port 443 is free, and vault.mydomain.com points to this machine
Continuing accepts Let's Encrypt's terms: https://letsencrypt.org/repository/
Continue? [y/N] y
✓ Certificate issued. Your vault is online at https://vault.mydomain.com
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
- **Port 443 without root.** It needs one `sudo` step, which we print and never run.
- **The moment before the first account on `public`.** Signups are open, so the wizard
  says plainly to create the account now, and how to close signups by hand.
- **`curl | sh`.** Convenient, but some people won't like it, so manual download
  stays documented.
- **WSL.** Windows browsers need the certificate imported by hand, and WSL can stop
  when idle.
- **A lost master password.** Nothing can fix this, so we work on prevention.
