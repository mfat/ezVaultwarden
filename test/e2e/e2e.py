#!/usr/bin/env python3
"""End-to-end tests against real Vaultwarden, Caddy, systemd and the bw CLI.

    python3 test/e2e/e2e.py local      install, backup, restore, recovery
    python3 test/e2e/e2e.py upgrade    update, rollback, failed updates, admin page
    python3 test/e2e/e2e.py all

See test/e2e/README.md first. These install real user services.
"""
import argparse
import base64
import hashlib
import json
import os
import pathlib
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time

from account import register

REPO = pathlib.Path(__file__).resolve().parents[2]
UNIT_DIR = pathlib.Path(
    os.environ.get("XDG_CONFIG_HOME", pathlib.Path.home() / ".config")) / "systemd" / "user"
URL = "https://localhost:8443"
MODULE = "github.com/mfat/ezVaultwarden"

# An older Vaultwarden to upgrade from, with a client that can still talk to it.
# bw 2026.10.1 can't log in to 1.37.3: it posts a key ID the server doesn't know.
OLD_VAULTWARDEN = "1.37.3"
OLD_DIGEST = "sha256:9a905c5cf5df61bb827da3805e160170741c0b53b0c63bb07e1afd3d2e21b1c8"
CLIENT_FOR_OLD = "2026.5.0"
CLIENT_CURRENT = "2026.10.1"

# Vaultwarden takes minutes to download on a slow line. A version folder that
# exists skips its download, so the tests keep verified ones here.
CACHE = pathlib.Path(os.environ.get("XDG_CACHE_HOME", pathlib.Path.home() / ".cache")) / "ezvaultwarden-e2e"


class Suite:
    def __init__(self, keep, cache):
        self.keep = keep
        self.cache = cache
        self.work = pathlib.Path(tempfile.mkdtemp(prefix="ezvw-e2e-"))
        self.home = self.work / "home"
        self.bindir = self.work / "bin"
        self.bindir.mkdir()
        self.release = self.work / "release"
        self.release.mkdir()
        self.env = dict(os.environ, EZVAULTWARDEN_HOME=str(self.home),
                        EZVAULTWARDEN_RELEASE_URL="http://127.0.0.1:18099/release")
        self.failures = []
        self.server = None
        self.creds = None
        self.bw_js = None
        self.profile = self.work / "bw"

    # --- reporting and commands ----------------------------------------------

    def check(self, label, ok, detail=""):
        print(("PASS " if ok else "FAIL ") + label, str(detail).replace("\n", " ")[:140], flush=True)
        if not ok:
            self.failures.append(label)

    def sh(self, cmd, timeout=240, stdin=None):
        r = subprocess.run(cmd, shell=True, executable="/bin/bash", env=self.env, input=stdin,
                           capture_output=True, text=True, timeout=timeout)
        return r.returncode, r.stdout + r.stderr

    def ez(self, args, timeout=240, stdin=None):
        return self.sh(f"{self.bindir}/ezvaultwarden {args}", timeout, stdin)

    def build(self, source, output, version):
        rc, out = self.sh(
            f"cd {source} && CGO_ENABLED=0 go build -trimpath "
            f"-ldflags '-X {MODULE}/internal/cli.Version={version}' -o {output} ./cmd/ezvaultwarden", 280)
        self.check(f"build {version}", rc == 0, out[-200:])
        if rc != 0:
            raise SystemExit(1)

    def seed(self, version_id):
        """Hands the install a cached version, which it treats as already verified."""
        if self.cache and (CACHE / version_id).exists():
            versions = self.home / "versions"
            versions.mkdir(parents=True, exist_ok=True)
            shutil.copytree(CACHE / version_id, versions / version_id)

    def install_client(self, version):
        cli = self.work / f"bw-{version}"
        rc, out = self.sh(f"npm install --prefix {cli} --no-audit --no-fund --ignore-scripts "
                          f"@bitwarden/cli@{version}", 280)
        self.check(f"bw {version} installed", rc == 0, out[-160:])
        self.bw_js = cli / "node_modules/@bitwarden/cli/build/bw.js"

    # --- server ---------------------------------------------------------------

    def init_and_register(self):
        log = self.work / "init.log"
        proc = subprocess.Popen(f"{self.bindir}/ezvaultwarden init --yes --no-trust > {log} 2>&1",
                                shell=True, executable="/bin/bash", env=self.env,
                                stdin=subprocess.DEVNULL)
        deadline = time.time() + 900
        text = ""
        while time.time() < deadline and proc.poll() is None:
            text = log.read_text() if log.exists() else ""
            if "Waiting for your account" in text:
                break
            time.sleep(3)
        self.check("init reaches the account step", "Waiting for your account" in text, text[-140:])
        if "Waiting for your account" not in text:
            raise SystemExit(1)
        self.creds = register(URL)
        proc.wait(timeout=90)
        text = log.read_text()
        self.check("signups close by themselves", "Signups closed" in text)
        self.check("init finishes", proc.returncode == 0, text[-100:])
        try:
            register(URL, email="intruder@example.test")
            self.check("second account refused", False)
        except RuntimeError as refused:
            self.check("second account refused", True, refused)

    def serve_release(self):
        self.server = subprocess.Popen(
            [sys.executable, "-m", "http.server", "18099", "--bind", "127.0.0.1"], cwd=self.work,
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        time.sleep(1)

    def server_version(self):
        rc, out = self.sh(f"{self.home}/current/vaultwarden --version")
        return out.strip().split("\n")[0]

    # --- client ---------------------------------------------------------------

    def bw(self, *args, session=None):
        env = dict(os.environ, BITWARDENCLI_APPDATA_DIR=str(self.profile), NODE_NO_WARNINGS="1",
                   NODE_EXTRA_CA_CERTS=str(self.home / "caddy/pki/authorities/local/root.crt"),
                   EZPW=self.creds["password"])
        if session:
            env["BW_SESSION"] = session
        return subprocess.run(["node", str(self.bw_js), *args, "--nointeraction"],
                              capture_output=True, env=env, timeout=120)

    def login(self):
        """A client with no cached copy, which is what restore tells people to get."""
        shutil.rmtree(self.profile, ignore_errors=True)
        self.profile.mkdir()
        self.bw("config", "server", URL)
        return self.bw("login", self.creds["email"], "--passwordenv", "EZPW", "--raw").stdout.decode().strip()

    def add_note(self, session, name):
        item = {"type": 2, "name": name, "notes": name + " body", "secureNote": {"type": 0}}
        encoded = base64.b64encode(json.dumps(item).encode()).decode()
        return json.loads(self.bw("create", "item", encoded, session=session).stdout)["id"]

    def add_attachment(self, session, item_id):
        payload = os.urandom(200_000)
        path = self.work / "photo.bin"
        path.write_bytes(payload)
        r = self.bw("create", "attachment", "--file", str(path), "--itemid", item_id, session=session)
        self.check("attachment stored", r.returncode == 0, r.stderr.decode()[:140])
        path.unlink()
        return hashlib.sha256(payload).hexdigest()

    def vault(self):
        """Item names and the attachment's hash, seen by a fresh client."""
        session = self.login()
        items = json.loads(self.bw("list", "items", session=session).stdout or b"[]")
        digest = None
        for item in items:
            for attachment in item.get("attachments", []):
                out = self.work / "photo.out"
                self.bw("get", "attachment", attachment["fileName"], "--itemid", item["id"],
                        "--output", str(out), session=session)
                if out.exists():
                    digest = hashlib.sha256(out.read_bytes()).hexdigest()
                    out.unlink()
        return sorted(i["name"] for i in items), digest

    # --- teardown --------------------------------------------------------------

    def uninstall(self):
        before = self.sh("loginctl show-user $USER -p Linger")[1].strip()
        rc, out = self.ez("uninstall --delete-data", stdin="delete my vault\n")
        self.check("uninstall", rc == 0, out[-90:])
        self.check("no units left", not list(UNIT_DIR.glob("ezvaultwarden-*")))
        print("linger:", before, "->", self.sh("loginctl show-user $USER -p Linger")[1].strip(), flush=True)

    def cleanup(self):
        if self.server:
            self.server.terminate()
        if list(UNIT_DIR.glob("ezvaultwarden-*")):
            self.ez("uninstall --delete-data", stdin="delete my vault\n")
        if not self.keep:
            shutil.rmtree(self.work, ignore_errors=True)


def preflight():
    if list(UNIT_DIR.glob("ezvaultwarden-*")):
        sys.exit("A real ezVaultwarden install is here. These tests would remove its services, so stopping.")
    for port in (8443, 18443, 18099):
        with socket.socket() as s:
            if s.connect_ex(("127.0.0.1", port)) == 0:
                sys.exit(f"Port {port} is in use.")
    for tool in ("go", "node", "npm", "loginctl", "systemctl"):
        if not shutil.which(tool):
            sys.exit(f"{tool} is needed.")


def run_local(t):
    """Install, data in, backup, damage, restore, then the failure cases."""
    t.build(REPO, t.bindir / "ezvaultwarden", "v0.0.0")
    t.install_client(CLIENT_CURRENT)
    t.seed(pinned_ids()[0])
    t.init_and_register()
    rc, out = t.ez("status")
    t.check("status after setup", "Running" in out and "signups closed" in out, out[:80])
    rc, out = t.ez("connect")
    t.check("connect names the certificate", "root.crt" in out)

    session = t.login()
    t.check("bw logs in with the printed certificate", bool(session))
    keep = t.add_note(session, "keep me")
    want = t.add_attachment(session, keep)

    rc, out = t.ez("backup")
    t.check("backup runs", rc == 0, out[:90])
    backup = next((t.home / "backups").glob("*.age"))
    t.check("backup is encrypted", backup.read_bytes().startswith(b"age-encryption.org"))
    t.check("server back after backup", t.sh("systemctl --user is-active ezvaultwarden-server")[1].strip() == "active")

    t.bw("delete", "item", keep, "--permanent", session=session)
    t.add_note(session, "made after backup")

    other = t.work / "other.key"
    other.write_text(subprocess.run([str(t.bindir / "ezvaultwarden"), "version"], capture_output=True).stdout.decode())
    rc, out = t.ez(f"restore --file {backup} --key-file {other} --yes")
    t.check("a non-key is refused", rc != 0, out[:90])
    t.check("vault up after refused restore", t.sh("systemctl --user is-active ezvaultwarden-server")[1].strip() == "active")

    key = t.home / "backup-key.pending"
    rc, out = t.ez(f"restore --file {backup} --key-file {key} --yes")
    t.check("restore succeeds", rc == 0, out[:110])
    t.check("restore warns about logged-in apps", "Log out and back in" in out)
    t.check("safety copy kept", bool(list(t.home.glob("data.before-restore-*"))))
    time.sleep(2)
    names, digest = t.vault()
    t.check("restored items match the backup", names == ["keep me"], names)
    t.check("attachment bytes match", digest == want)

    rc, out = t.sh("pkill -9 -x vaultwarden; sleep 6; "
                   f"curl -sk -m 5 -o /dev/null -w '%{{http_code}}' {URL}/alive")
    t.check("recovers from a crash", out.strip().endswith("200"), out)
    t.ez("stop")
    t.check("status shows stopped", "Stopped" in t.ez("status")[1])
    t.ez("start")
    time.sleep(3)
    t.check("start brings it back", '"running": true' in t.ez("status --json")[1])
    t.sh("systemctl --user stop ezvaultwarden-proxy")
    t.check("status notices one service down", "Stopped" in t.ez("status")[1])
    t.sh("systemctl --user start ezvaultwarden-proxy")
    rc, out = t.ez("init --yes --no-trust")
    t.check("second init refused", rc != 0 and "already set up" in out, out[:80])
    rc, out = t.sh("systemctl --user start ezvaultwarden-backup.service; sleep 2; "
                   "systemctl --user show ezvaultwarden-backup.service -p Result")
    t.check("scheduled backup runs", "Result=success" in out, out)
    t.check("scheduled backup made a file", len(list((t.home / "backups").glob("*.age"))) == 2)
    t.uninstall()


def pinned_ids():
    text = (REPO / "internal/fetch/pins.go").read_text()
    new = re.search(r'VaultwardenVersion = "([^"]+)"', text).group(1)
    caddy = re.search(r'CaddyVersion += "([^"]+)"', text).group(1)
    return f"{new}+caddy{caddy}", f"{OLD_VAULTWARDEN}+caddy{caddy}"


def old_source(work):
    """This repo, pinned to the older Vaultwarden."""
    src = work / "old-src"
    if src.exists():
        return src
    shutil.copytree(REPO, src, ignore=shutil.ignore_patterns(".git"))
    pins = src / "internal/fetch/pins.go"
    text = pins.read_text()
    text = re.sub(r'VaultwardenVersion = "[^"]+"', f'VaultwardenVersion = "{OLD_VAULTWARDEN}"', text)
    text = re.sub(r'Digest:( +)"sha256:[0-9a-f]+"', lambda m: f'Digest:{m.group(1)}"{OLD_DIGEST}"', text)
    pins.write_text(text)
    return src


def fill_cache():
    """Downloads whatever's missing, both versions at once."""
    new_id, old_id = pinned_ids()
    CACHE.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="ezvw-cache-") as work:
        sources = {new_id: REPO, old_id: old_source(pathlib.Path(work))}
        jobs = {version_id: subprocess.Popen(["go", "run", "./test/e2e/cache", str(CACHE / version_id)], cwd=source)
                for version_id, source in sources.items() if not (CACHE / version_id).exists()}
        if jobs:
            print(f"Downloading into {CACHE}: {', '.join(jobs)}", flush=True)
        for version_id, job in jobs.items():
            if job.wait() != 0:
                sys.exit(f"Couldn't download {version_id}.")


def publish(t, version):
    (t.release / "VERSION").write_text(version + "\n")
    t.sh(f"cd {t.release} && sha256sum ezvaultwarden_linux_* > checksums.txt")


def run_upgrade(t):
    """A real upgrade from an older Vaultwarden, then everything that can go wrong."""
    arch = {"x86_64": "amd64", "aarch64": "arm64"}[os.uname().machine]
    t.build(old_source(t.work), t.bindir / "ezvaultwarden", "v0.1.9")
    t.build(REPO, t.release / f"ezvaultwarden_linux_{arch}", "v0.2.0")
    publish(t, "v0.2.0")
    t.serve_release()
    t.install_client(CLIENT_FOR_OLD)
    new_id, old_id = pinned_ids()
    t.seed(old_id)
    t.init_and_register()
    t.seed(new_id)

    session = t.login()
    t.check("old client logs in to old server", bool(session))
    keep = t.add_note(session, "keep me")
    want = t.add_attachment(session, keep)
    t.check(f"starts on {OLD_VAULTWARDEN}", OLD_VAULTWARDEN in t.server_version(), t.server_version())

    rc, out = t.ez("update --check")
    t.check("--check reports the release", "v0.1.9 → v0.2.0" in out, out)
    t.check("--check changes nothing", OLD_VAULTWARDEN in t.server_version())

    started = time.time()
    rc, out = t.ez("update --yes", 900)
    t.check("update succeeds", rc == 0, f"{time.time() - started:.0f}s {out[-90:]}")
    t.check("program replaced", "v0.2.0" in t.ez("version")[1])
    new_version = t.server_version()
    t.check("server upgraded", OLD_VAULTWARDEN not in new_version, new_version)
    names, digest = t.vault()
    t.check("data survives the upgrade", names == ["keep me"] and digest == want, names)
    snapshot = t.home / "data.before-update"
    t.check("snapshot is private", oct(snapshot.stat().st_mode)[-3:] == "700")
    t.check("second update does nothing", "Up to date" in t.ez("update --yes")[1])

    t.add_note(t.login(), "made on new version")
    rc, out = t.ez("rollback --yes")
    t.check("rollback succeeds", rc == 0, out[-100:])
    t.check("back on the old version", OLD_VAULTWARDEN in t.server_version(), t.server_version())
    time.sleep(1)
    names, digest = t.vault()
    t.check("rollback restores pre-update data", names == ["keep me"] and digest == want, names)
    t.check("later changes set aside", bool(list(t.home.glob("data.replaced-*"))))
    t.check("second rollback refused", t.ez("rollback --yes")[0] != 0)

    # A new version that reports the right number and never serves.
    t.add_note(t.login(), "before broken update")
    new_id = next(p for p in (t.home / "versions").iterdir() if OLD_VAULTWARDEN not in p.name)
    fake = new_id / "vaultwarden"
    number = new_version.split()[-1]
    fake.write_text(f'#!/bin/sh\n[ "$1" = --version ] && echo Vaultwarden {number} && exit 0\nexit 1\n')
    fake.chmod(0o755)
    rc, out = t.ez("update --yes", 300)
    t.check("broken update reports failure", rc != 0 and "running as it was" in out, out[-110:])
    t.check("broken update returns to the old version", OLD_VAULTWARDEN in t.server_version())
    time.sleep(1)
    names, digest = t.vault()
    t.check("broken update loses nothing", names == ["before broken update", "keep me"] and digest == want, names)

    (t.release / "VERSION").write_text("v0.3.0\n")
    (t.release / f"ezvaultwarden_linux_{arch}").write_text("tampered")
    rc, out = t.ez("update --yes")
    t.check("tampered release refused", rc != 0 and "checksum" in out, out[-90:])
    t.check("program untouched", "v0.2.0" in t.ez("version")[1])

    run_admin(t)
    t.uninstall()


def run_admin(t):
    work = t.work
    rc, out = t.sh(f"""
cd {work}; rm -f afifo aout; mkfifo afifo
{t.bindir}/ezvaultwarden admin < afifo > aout 2>&1 &
exec 3> afifo
for i in $(seq 1 60); do grep -q 'Press Enter' aout && break; sleep 1; done
PW=$(grep 'Password, for this session' aout | awk '{{print $NF}}')
echo "hash in env: $(grep -c "ADMIN_TOKEN='\\$argon2id" {t.home}/vaultwarden.env)"
echo "password on disk: $(grep -l -- "$PW" {t.home}/vaultwarden.env {t.home}/state.json | wc -l)"
echo "wrong password: $(curl -sk -o /dev/null -c badjar -w '%{{http_code}}' -d token=wrong {URL}/admin) cookies $(grep -c VW_ADMIN badjar)"
curl -sk -o /dev/null -c jar -d "token=$PW" {URL}/admin
echo "cookies $(grep -c VW_ADMIN jar)"
echo "users page: $(curl -sk -o /dev/null -w '%{{http_code}}' -b jar {URL}/admin/users/overview)"
echo "save: $(curl -sk -o /dev/null -w '%{{http_code}}' -b jar -H 'Content-Type: application/json' -d '{{"signups_allowed":true}}' {URL}/admin/config)"
echo >&3
exec 3>&-
for i in $(seq 1 60); do grep -q 'Admin page closed' aout && break; sleep 1; done
echo "after close: $(curl -sk -o /dev/null -w '%{{http_code}}' -b jar {URL}/admin/users/overview)"
echo "token in env: $(grep -c ADMIN_TOKEN {t.home}/vaultwarden.env)"
""", 200)
    t.check("printed password logs in", "users page: 200" in out, out[:60])
    t.check("wrong password refused", "wrong password: 401 cookies 0" in out)
    t.check("password never on disk", "password on disk: 0" in out)
    t.check("hash in the environment file", "hash in env: 1" in out)
    t.check("page gone after close", "after close: 404" in out and "token in env: 0" in out)
    rc, out = t.ez("status")
    t.check("status warns when saved settings reopen signups", "Signups are OPEN" in out)
    t.check("status doesn't contradict the warning", "signups closed" not in out)
    (t.home / "data/config.json").unlink()
    t.ez("admin --close")
    t.check("warning clears", "OPEN" not in t.ez("status")[1])


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("suite", choices=["local", "upgrade", "all"])
    parser.add_argument("--keep", action="store_true", help="keep the work folder for debugging")
    parser.add_argument("--no-cache", action="store_true", help="download everything, as a real install does")
    args = parser.parse_args()
    preflight()
    if not args.no_cache:
        fill_cache()
    failures = []
    for name, run in (("local", run_local), ("upgrade", run_upgrade)):
        if args.suite not in (name, "all"):
            continue
        print(f"== {name}", flush=True)
        suite = Suite(args.keep, not args.no_cache)
        try:
            run(suite)
        finally:
            suite.cleanup()
        failures += suite.failures
    print("FAILED: " + ", ".join(failures) if failures else "All checks passed.")
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
