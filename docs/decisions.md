# Decisions

Why it's built this way. Each entry says what would make us change our mind.

## Native binaries by default, containers as an option

The point of the tool is no prerequisites, so on Linux it runs static binaries as
systemd user services. There's no daemon, no root, and nothing to install first.

Containers still win on isolation, easy rollback, and Mac and Windows support, so
Docker and Podman are planned behind the same `Runtime` interface. For `public` mode
we should recommend Podman where it's available, because isolation matters most there.

Docker can also publish ports past UFW and firewalld, which is a real trap for a
password server.

## Extract the binary from the official image

Vaultwarden publishes no binaries, and its wiki documents extracting them from the
Alpine images. We do that over the registry API, so no container runtime is needed.
The risk is a change in the image layout. Pinning a tested digest per release limits
it to a failed test before release, not a break for users.

Distro packages exist too. A `package` runtime is a possible later addition.

## One image for the server and web vault

The image contains a web vault the Vaultwarden project built to match. Pinning one
digest is simpler than pairing it with the separate `bw_web_builds` release, and
guarantees a tested pair.

## Go, not Python

With Docker gone, the appeal is one download that just runs. A static Go binary
keeps that. Python would add its own install step on the machines this is meant to
make easy. The original plan was Python, for sshPilot's stack. This changed once the
tool stopped being a thin wrapper around Compose.

## Caddy for TLS

We never write certificate code. Caddy provides a local CA now and ACME later, and
is the one thing that changes between access modes. Vaultwarden's own TLS worked in
testing, but a proxy is the better long-running setup.

`bw` refuses plain HTTP, even on localhost, so TLS isn't optional in `local` mode.

## `tailscale serve`, not Caddy, for `tailnet` mode

Caddy can fetch `*.ts.net` certificates from `tailscaled`, but as a non-root user it
needs `TS_PERMIT_CERT_UID` in the root-owned `/etc/default/tailscaled`. `tailscale
serve` needs one `sudo tailscale set --operator=$USER` instead, renews its own
certificate and survives reboots with `--bg`. The cost is a second way in that
bypasses Caddy. We ask the user to run the `sudo` step, and never run it ourselves.

If Caddy has to see all traffic, say for rate limits, switch to Caddy's integration.

## `lego` for the DNS challenge

Official Caddy has no DNS plugins, and a custom build would lose its published
checksums, so `lego` gets the certificate and Caddy just serves the files. It's only
downloaded on the first `expose lan`, and stays out of `VersionID`, so people who never
use it don't pay for it and a `lego` bump doesn't force a Vaultwarden update.

The token stays in a file `lego` reads itself, so it's never in a process list. We
register no email: Let's Encrypt has stopped sending expiry mail, so `status` warns
instead.

## Hidden: Let's Encrypt on a LAN

`expose lan` works and stays in the code, but it's off the menu and out of the help.
The Cloudflare token, the router problems and the home-only reach made it the hardest
option to explain. The notes below are why it's built the way it is.

## The token stays, for now

Nginx Proxy Manager and similar tools also ask for a pasted token, because creating one
takes a logged-in person. The page can't preselect a domain: it ignored a domain name in
`zoneId`, and we couldn't know the zone ID before having a token anyway. So we cut
everything around it: a pre-filled page, a check
before saving, and a DNS record we create and keep up to date. Only records carrying
our comment are ever changed or deleted, so a name already in use is left alone.

Cloudflare's OAuth with PKCE could replace the paste with an Approve click. It needs a
loopback redirect and a DNS scope to be confirmed, has no device flow for headless
machines, and reaches every domain in the account rather than one, so the token would
stay as the fallback. DNS-PERSIST-01 would be better still once Let's Encrypt offers it.

## One setup, and the user decides

We don't detect servers or homes. Detection guessed wrong on clouds with mapped addresses,
and a second menu was more to explain. The menu is the same everywhere, and the checks
follow from the answer. A private address on public access warns rather than refuses,
because a cloud server can look like that and still work.

## Public access through Caddy on port 443

Caddy's automatic HTTPS needs only a domain pointing here and port 443. That means no
token, no `lego`, any DNS provider and a normal address. The cost is one `sudo` step when
not running as root, which we print and never run. Port 80 stays closed, as the TLS check
works over 443, so plain `http://` links fail instead of redirecting.

## Public signups stay open until the first account

A public site can't use an SSH tunnel for ordinary users, and a temporary password would
be more to explain. So the wizard says plainly to create the account now, closes signups
automatically when it appears, and teaches `signups close` for stopping partway. The price
is a short window where someone else could register. Two-step login can't be required
before the account exists, so `status` warns until it's on.

## Backups encrypted to a public key

The first design used a word passphrase. That would have meant storing a secret on
the server for the daily timer, or asking a person every night. With a public key
the server can make backups it can't read. The cost is a long `AGE-SECRET-KEY-...`
string, which is fine for a recovery kit.

## Rollback needs a plain copy of the data

Automatic rollback can't use the encrypted backups, because the server deliberately
can't open them. So `update` also keeps `data.before-update`. It's no more exposed
than the live `data/` folder beside it.

## Binaries and data roll back together

A newer Vaultwarden may migrate the database so the older one can't read it. Rolling
back only the binary could fail, or worse. The price is that changes since the update
leave the vault, so they're set aside in `data.replaced-*` and never deleted.

## Versions are compiled in

A release is a tested combination, so the program pins exact digests. This is why
`update` must replace the program first. It means a new Vaultwarden needs a new
ezVaultwarden release, which keeps "what we tested" and "what users run" identical.

## A version folder existing is proof it was verified

A folder under `versions/` only appears by rename once complete, so `update` trusts
it. This also let the tests plant a broken version without any test-only hooks.

## Signups close by watching the database

There's no setting for "first account only", so `init` reads the `users` table and
restarts with signups off. Reading SQLite needs `modernc.org/sqlite`, which adds
weight to the binary, but it's far simpler than driving the admin API.

## The admin page stays off

A page that doesn't exist can't be attacked. It opens on request with a one-time
password, stored only as an Argon2id hash, and closes when you're done.

## `config.json` is detected, not fought

Settings saved in the admin page beat the environment, so ezVaultwarden can be
overridden silently. We warn in `status` instead of rewriting a file Vaultwarden
owns. If people get caught by this, we could offer to fix it.

## A master password can't be reset

This is how end-to-end encryption works, and it's the same on Bitwarden's service.
Backups don't help, because they hold the same encrypted data. So the design centres
on prevention: the recovery kit, and suggesting emergency access.

## Not a prerelease

Releases are normal GitHub releases, because the `latest/download` links that
`install.sh` uses skip prereleases. Being early is signalled by the 0.x version and
the README warning.

## `install.sh` keeps everything in `main`

A `curl | sh` cut off midway must run nothing, so the script only acts on its last
line. It's also why manual download stays documented, for people who'd rather not
pipe to a shell.

## Windows through WSL, not natively

There's no Windows build of Vaultwarden, and building one is a large job for a small
gain. WSL works for local use, and Docker Desktop support is the realistic next step.
