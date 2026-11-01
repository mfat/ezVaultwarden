package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mfat/ezVaultwarden/internal/backup"
	"github.com/mfat/ezVaultwarden/internal/config"
)

type fakeRuntime struct {
	running  bool
	stops    int
	removed  bool
	startErr error
}

func (*fakeRuntime) Name() string              { return "fake" }
func (*fakeRuntime) Available() error          { return nil }
func (*fakeRuntime) Install(string) error      { return nil }
func (f *fakeRuntime) Start() error            { f.running = true; return nil }
func (f *fakeRuntime) Stop() error             { f.running = false; return nil }
func (f *fakeRuntime) StopVault() error        { f.running = false; f.stops++; return nil }
func (f *fakeRuntime) RestartVault() error     { f.running = true; return nil }
func (*fakeRuntime) RestartProxy() error       { return nil }
func (f *fakeRuntime) Running() bool           { return f.running }
func (*fakeRuntime) Logs(io.Writer, int) error { return nil }
func (f *fakeRuntime) Remove() error           { f.removed = true; return nil }
func (f *fakeRuntime) StartVault() error {
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}

type harness struct {
	*app
	rt     *fakeRuntime
	output *bytes.Buffer
	secret string
}

func reader(input string) *bufio.Reader { return bufio.NewReader(strings.NewReader(input)) }

func setup(t *testing.T, input string) *harness {
	t.Helper()
	paths := config.Paths{Root: t.TempDir()}
	if err := os.MkdirAll(paths.Data(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Data(), "db.sqlite3"), []byte("vault"), 0o600); err != nil {
		t.Fatal(err)
	}
	recipient, secret, err := backup.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	state := config.State{Mode: "local", Port: 8443, SignupsClosed: true, BackupRecipient: recipient}
	if err := config.Save(paths, state); err != nil {
		t.Fatal(err)
	}
	rt := &fakeRuntime{running: true}
	out := &bytes.Buffer{}
	return &harness{
		app: &app{
			out: out, in: reader(input), paths: paths, rt: rt,
			now:    func() time.Time { return time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC) },
			health: func(context.Context, config.State) error { return nil },
		},
		rt: rt, output: out, secret: secret,
	}
}

func TestBackupBringsTheServerBack(t *testing.T) {
	h := setup(t, "")
	if err := h.backup(nil); err != nil {
		t.Fatal(err)
	}
	if h.rt.stops != 1 {
		t.Fatalf("server stopped %d times, want it at rest for the copy", h.rt.stops)
	}
	if !h.rt.running {
		t.Fatal("server left down")
	}
	if !strings.Contains(h.output.String(), "this disk") {
		t.Fatalf("no warning about a single copy:\n%s", h.output)
	}
}

func TestFailedBackupStillBringsTheServerBack(t *testing.T) {
	h := setup(t, "")
	s, _ := h.load()
	s.BackupRecipient = "not a key"
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
	if err := h.backup(nil); err == nil {
		t.Fatal("expected a failure")
	}
	if !h.rt.running {
		t.Fatal("server left down after a failed backup")
	}
}

func TestBackupOfAStoppedServerLeavesItStopped(t *testing.T) {
	h := setup(t, "")
	h.rt.running = false
	if err := h.backup([]string{"--auto"}); err != nil {
		t.Fatal(err)
	}
	if h.rt.running {
		t.Fatal("started a server the user had stopped")
	}
}

func TestBackupTargetGetsACopy(t *testing.T) {
	h := setup(t, "")
	target := filepath.Join(t.TempDir(), "usb")
	if err := h.backup([]string{"--target", target}); err != nil {
		t.Fatal(err)
	}
	files, _ := backup.List(target)
	if len(files) != 1 {
		t.Fatalf("target holds %v", files)
	}
	if strings.Contains(h.output.String(), "this disk") {
		t.Fatal("warned about a single copy with a target set")
	}
}

func TestRestoreWithTheWrongKeyLeavesTheVaultRunning(t *testing.T) {
	h := setup(t, "")
	if err := h.backup([]string{"--auto"}); err != nil {
		t.Fatal(err)
	}
	files, _ := backup.List(h.paths.Backups())
	_, other, _ := backup.NewKey()
	keyFile := filepath.Join(t.TempDir(), "key")
	os.WriteFile(keyFile, []byte(other), 0o600)

	err := h.restore([]string{"--file", files[0], "--key-file", keyFile, "--yes"})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !h.rt.running {
		t.Fatal("server left down after a refused restore")
	}
	got, _ := os.ReadFile(filepath.Join(h.paths.Data(), "db.sqlite3"))
	if string(got) != "vault" {
		t.Fatalf("vault changed: %q", got)
	}
}

func TestRestoreDeclinedChangesNothing(t *testing.T) {
	h := setup(t, "n\n")
	if err := h.backup([]string{"--auto"}); err != nil {
		t.Fatal(err)
	}
	files, _ := backup.List(h.paths.Backups())
	keyFile := filepath.Join(t.TempDir(), "key")
	os.WriteFile(keyFile, []byte(h.secret), 0o600)
	stops := h.rt.stops

	if err := h.restore([]string{"--file", files[0], "--key-file", keyFile}); err == nil {
		t.Fatal("expected nothing to change")
	}
	if h.rt.stops != stops {
		t.Fatal("server was stopped for a declined restore")
	}
}

func TestUninstallKeepsTheVaultByDefault(t *testing.T) {
	h := setup(t, "")
	if err := h.uninstall(nil); err != nil {
		t.Fatal(err)
	}
	if !h.rt.removed {
		t.Fatal("service not removed")
	}
	if _, err := os.Stat(filepath.Join(h.paths.Data(), "db.sqlite3")); err != nil {
		t.Fatal("vault was deleted")
	}
}

func TestUninstallPointsAtACertificateAnOlderVersionLeftBehind(t *testing.T) {
	saved := oldAnchors
	t.Cleanup(func() { oldAnchors = saved })
	left := filepath.Join(t.TempDir(), "ezvaultwarden.crt")
	if err := os.WriteFile(left, []byte("cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldAnchors = []struct{ path, refresh string }{{left, "update-ca-certificates"}}

	h := setup(t, "")
	if err := h.uninstall(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "sudo rm -f "+left) {
		t.Fatalf("nothing says how to remove it: %s", h.output)
	}
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("uninstall touched the system's store: %v", err)
	}
}

func TestDeletingTheVaultNeedsTheExactPhrase(t *testing.T) {
	h := setup(t, "yes\n")
	if err := h.uninstall([]string{"--delete-data"}); err == nil {
		t.Fatal("expected a refusal")
	}
	if h.rt.removed {
		t.Fatal("service removed after a refusal")
	}
	if _, err := os.Stat(filepath.Join(h.paths.Data(), "db.sqlite3")); err != nil {
		t.Fatal("vault was deleted")
	}

	h = setup(t, deletePhrase+"\n")
	if err := h.uninstall([]string{"--delete-data"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.paths.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("vault still there")
	}
}

func TestStatusWarnsWhenSignupsAreOpen(t *testing.T) {
	h := setup(t, "")
	s, _ := h.load()
	s.SignupsClosed = false
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
	if err := h.status(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "signups OPEN") {
		t.Fatalf("no warning:\n%s", h.output)
	}
}

func TestCommandsBeforeInitSayWhatToDo(t *testing.T) {
	h := setup(t, "")
	os.Remove(h.paths.State())
	if err := h.status(nil); !errors.Is(err, config.ErrNotSetUp) {
		t.Fatalf("got %v", err)
	}
}
