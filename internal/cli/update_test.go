package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mfat/ezVaultwarden/internal/config"
	"github.com/mfat/ezVaultwarden/internal/fetch"
)

const oldID = "1.0.0+caddy1.0.0"

// updatable is an install on an old version, with this build's version
// already verified on disk so nothing is downloaded.
func updatable(t *testing.T) *harness {
	t.Helper()
	h := setup(t, "")
	for _, id := range []string{oldID, fetch.VersionID(), "1.0.0-even-older"} {
		if err := os.MkdirAll(filepath.Join(h.paths.Versions(), id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(h.paths.Versions(), oldID), h.paths.Current()); err != nil {
		t.Fatal(err)
	}
	s, _ := h.load()
	s.Version = "1.0.0"
	if err := config.Save(h.paths, s); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) vaultContent(t *testing.T) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(h.paths.Data(), "db.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func TestUpdateSwitchesVersionsAndKeepsARollback(t *testing.T) {
	h := updatable(t)
	s, _ := h.load()
	if err := h.applyUpdate(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if h.currentID() != fetch.VersionID() {
		t.Fatalf("running %s", h.currentID())
	}
	s, _ = h.load()
	if s.Version != fetch.VaultwardenVersion || s.Previous == nil || s.Previous.ID != oldID {
		t.Fatalf("state = %+v", s)
	}
	if !h.rt.running {
		t.Fatal("server left down")
	}
	if _, err := os.Stat(filepath.Join(h.paths.Versions(), oldID)); err != nil {
		t.Fatal("removed the version rollback needs")
	}
	if _, err := os.Stat(filepath.Join(h.paths.Versions(), "1.0.0-even-older")); err == nil {
		t.Fatal("old versions pile up")
	}
	files, _ := filepath.Glob(filepath.Join(h.paths.Backups(), "*.age"))
	if len(files) != 1 {
		t.Fatalf("no encrypted backup before updating: %v", files)
	}
}

func TestFailedUpdateRestoresBinariesAndData(t *testing.T) {
	h := updatable(t)
	h.health = func(context.Context, config.State) error {
		// Stands in for the new server migrating the database, then failing.
		if h.currentID() == fetch.VersionID() {
			os.WriteFile(filepath.Join(h.paths.Data(), "db.sqlite3"), []byte("migrated"), 0o600)
			return errors.New("not healthy")
		}
		return nil
	}
	s, _ := h.load()
	err := h.applyUpdate(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "running as it was") {
		t.Fatalf("got %v", err)
	}
	if h.currentID() != oldID {
		t.Fatalf("running %s", h.currentID())
	}
	if got := h.vaultContent(t); got != "vault" {
		t.Fatalf("data after rollback = %q", got)
	}
	s, _ = h.load()
	if s.Version != "1.0.0" || s.Previous != nil {
		t.Fatalf("state = %+v", s)
	}
	if !h.rt.running {
		t.Fatal("server left down")
	}
	if !strings.Contains(h.output.String(), "Rolled back") {
		t.Fatalf("no explanation:\n%s", h.output)
	}
	// The failed version's data is kept rather than destroyed.
	kept, _ := filepath.Glob(h.paths.Data() + ".replaced-*")
	if len(kept) != 1 {
		t.Fatalf("kept %v", kept)
	}
}

func TestFailedRollbackSaysWhereTheDataIs(t *testing.T) {
	h := updatable(t)
	h.health = func(context.Context, config.State) error { return errors.New("never healthy") }
	s, _ := h.load()
	err := h.applyUpdate(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "rolling back failed") {
		t.Fatalf("got %v", err)
	}
}

func TestUpdateWithNothingToDoLeavesTheServerAlone(t *testing.T) {
	h := updatable(t)
	os.Remove(h.paths.Current())
	os.Symlink(filepath.Join(h.paths.Versions(), fetch.VersionID()), h.paths.Current())
	s, _ := h.load()
	if err := h.applyUpdate(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if h.rt.stops != 0 || !h.rt.running {
		t.Fatal("touched a server with nothing to update")
	}
}

func TestRollbackReturnsToTheEarlierVersionAndData(t *testing.T) {
	h := updatable(t)
	s, _ := h.load()
	if err := h.applyUpdate(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(h.paths.Data(), "db.sqlite3"), []byte("later change"), 0o600)
	if err := h.rollback([]string{"--yes"}); err != nil {
		t.Fatal(err)
	}
	if h.currentID() != oldID {
		t.Fatalf("running %s", h.currentID())
	}
	if got := h.vaultContent(t); got != "vault" {
		t.Fatalf("data = %q", got)
	}
	kept, _ := filepath.Glob(h.paths.Data() + ".replaced-*")
	if len(kept) != 1 {
		t.Fatalf("later changes weren't set aside: %v", kept)
	}
}

func TestRollbackNeedsSomethingToGoBackTo(t *testing.T) {
	h := updatable(t)
	if err := h.rollback([]string{"--yes"}); err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestAStoppedServerIsStoppedAgainAfterUpdating(t *testing.T) {
	h := updatable(t)
	h.rt.running = false
	s, _ := h.load()
	if err := h.applyUpdate(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if h.rt.running {
		t.Fatal("left a stopped server running")
	}
}

func TestAFailedCopyKeepsTheEarlierRollback(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	h := updatable(t)
	snapshot := h.paths.Snapshot()
	if err := os.MkdirAll(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "keep"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.paths.Data(), "unreadable"), nil, 0o000); err != nil {
		t.Fatal(err)
	}
	s, _ := h.load()
	if err := h.applyUpdate(context.Background(), s); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(filepath.Join(snapshot, "keep")); err != nil {
		t.Fatalf("lost the earlier rollback: %v", err)
	}
	if !h.rt.running {
		t.Fatal("left the server down")
	}
}
