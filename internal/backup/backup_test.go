package backup

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)

func dataDir(t *testing.T, root string) string {
	t.Helper()
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(data, "attachments", "abc"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"db.sqlite3":               "database",
		"rsa_key.pem":              "key",
		"attachments/abc/file.bin": "attachment",
	} {
		if err := os.WriteFile(filepath.Join(data, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return data
}

func TestRoundTripKeepsAttachmentsAndThePreviousData(t *testing.T) {
	root := t.TempDir()
	data := dataDir(t, root)
	recipient, secret, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	file, err := Create(data, filepath.Join(root, "backups"), recipient, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "db.sqlite3"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}

	keep, err := Restore(file, secret+"\n", data, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(data, "attachments", "abc", "file.bin"))
	if err != nil || string(got) != "attachment" {
		t.Fatalf("attachment not restored: %q %v", got, err)
	}
	if got, _ := os.ReadFile(filepath.Join(data, "db.sqlite3")); string(got) != "database" {
		t.Fatalf("database not restored: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(keep, "db.sqlite3")); string(got) != "changed" {
		t.Fatalf("replaced data not kept: %q", got)
	}
}

func TestBackupContentsAreNotReadableWithoutTheKey(t *testing.T) {
	root := t.TempDir()
	data := dataDir(t, root)
	recipient, _, _ := NewKey()
	file, err := Create(data, filepath.Join(root, "backups"), recipient, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	if bytes.Contains(raw, []byte("attachment")) || bytes.Contains(raw, []byte("db.sqlite3")) {
		t.Fatal("backup leaks plaintext")
	}
}

func TestWrongKeyLeavesTheVaultAlone(t *testing.T) {
	root := t.TempDir()
	data := dataDir(t, root)
	recipient, _, _ := NewKey()
	_, other, _ := NewKey()
	file, err := Create(data, filepath.Join(root, "backups"), recipient, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(file, other, data, now); err == nil {
		t.Fatal("restore with the wrong key succeeded")
	}
	if got, _ := os.ReadFile(filepath.Join(data, "db.sqlite3")); string(got) != "database" {
		t.Fatalf("vault changed: %q", got)
	}
}

func TestSecondBackupInTheSameSecondDoesNotOverwriteTheFirst(t *testing.T) {
	root := t.TempDir()
	data := dataDir(t, root)
	recipient, _, _ := NewKey()
	dir := filepath.Join(root, "backups")
	first, err := Create(data, dir, recipient, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Create(data, dir, recipient, now)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("same path used twice")
	}
}

func TestPruneKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	for _, day := range []string{"20261030", "20261031", "20261101"} {
		name := "vault-" + day + "-030000" + suffix
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Prune(dir, 2); err != nil {
		t.Fatal(err)
	}
	files, _ := List(dir)
	if len(files) != 2 || !strings.Contains(files[0], "20261101") {
		t.Fatalf("kept %v", files)
	}
}

func TestExtractRefusesPathsOutsideTheFolder(t *testing.T) {
	for _, name := range []string{"../evil", "/etc/evil", "a/../../evil"} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte("x"))
		tw.Close()
		if err := extractTar(&buf, t.TempDir()); err == nil {
			t.Fatalf("%q was accepted", name)
		}
	}
}
