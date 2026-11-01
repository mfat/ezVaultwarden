package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mfat/ezVaultwarden/internal/config"
)

func TestScheduledBackupUsesTheSameFolder(t *testing.T) {
	units := Units(config.Paths{Root: "/custom/root"}, "/usr/bin/ezvaultwarden")
	if !strings.Contains(units[backupUnit], "Environment=EZVAULTWARDEN_HOME=/custom/root\n") {
		t.Fatalf("the timer would look in the default folder:\n%s", units[backupUnit])
	}
}

func TestServicesPointAtTheCurrentVersion(t *testing.T) {
	units := Units(config.Paths{Root: "/r"}, "/bin/ez")
	for _, name := range []string{vaultUnit, caddyUnit} {
		if !strings.Contains(units[name], "/r/current/") {
			t.Fatalf("%s bypasses the current symlink:\n%s", name, units[name])
		}
	}
}

func TestRemoveStopsEveryUnitEvenIfOneNeverExisted(t *testing.T) {
	bin, cfg := t.TempDir(), t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	// Like systemctl, a single unknown unit fails the whole command and stops nothing.
	script := "#!/bin/sh\ncase \"$*\" in *cert.timer*ezvaultwarden*) exit 1;; esac\necho \"$*\" >> " + calls + "\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", cfg)

	if err := (Systemd{Paths: config.Paths{Root: t.TempDir()}}).Remove(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range []string{vaultUnit, caddyUnit, timerUnit} {
		if !strings.Contains(string(raw), "disable --now "+unit) {
			t.Fatalf("%s kept running:\n%s", unit, raw)
		}
	}
}
