package runtime

import (
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
