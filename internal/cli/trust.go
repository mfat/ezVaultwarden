package cli

import (
	"fmt"
	"os"
)

// v0.2.0 and earlier could add the local certificate to the system's store.
// We no longer touch that store, so uninstall only says how to clear a copy
// left behind.
var oldAnchors = []struct{ path, refresh string }{
	{"/usr/local/share/ca-certificates/ezvaultwarden.crt", "update-ca-certificates"},   // Debian, Ubuntu
	{"/etc/pki/ca-trust/source/anchors/ezvaultwarden.crt", "update-ca-trust"},          // Fedora, RHEL
	{"/etc/ca-certificates/trust-source/anchors/ezvaultwarden.crt", "update-ca-trust"}, // Arch
}

func (a *app) warnAboutOldAnchors() {
	for _, anchor := range oldAnchors {
		if _, err := os.Stat(anchor.path); err != nil {
			continue
		}
		a.warn("An older version added this server's certificate to your system. Remove it with:")
		fmt.Fprintf(a.out, "  sudo rm -f %s && sudo %s\n", anchor.path, anchor.refresh)
	}
}
