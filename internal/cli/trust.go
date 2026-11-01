package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// A trust store, as each family of distributions lays it out.
type trustStore struct {
	refresh string
	anchor  string
}

var trustStores = []trustStore{
	{"update-ca-certificates", "/usr/local/share/ca-certificates/ezvaultwarden.crt"},   // Debian, Ubuntu
	{"update-ca-trust", "/etc/pki/ca-trust/source/anchors/ezvaultwarden.crt"},          // Fedora, RHEL
	{"update-ca-trust", "/etc/ca-certificates/trust-source/anchors/ezvaultwarden.crt"}, // Arch
}

func findTrustStore() (trustStore, error) {
	for _, store := range trustStores {
		if _, err := exec.LookPath(store.refresh); err != nil {
			continue
		}
		if _, err := os.Stat(parentDir(store.anchor)); err == nil {
			return store, nil
		}
	}
	return trustStore{}, errors.New("this system's certificate store isn't one we recognise")
}

func parentDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

func sudo(args ...string) error {
	cmd := exec.Command("sudo", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo %s: %w", args[0], err)
	}
	return nil
}

func trustCertificate(cert string) error {
	store, err := findTrustStore()
	if err != nil {
		return err
	}
	if err := sudo("install", "-m", "0644", cert, store.anchor); err != nil {
		return err
	}
	return sudo(store.refresh)
}

func untrustCertificate() error {
	store, err := findTrustStore()
	if err != nil {
		return err
	}
	if err := sudo("rm", "-f", store.anchor); err != nil {
		return err
	}
	return sudo(store.refresh)
}
