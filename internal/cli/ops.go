package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mfat/ezVaultwarden/internal/backup"
	"github.com/mfat/ezVaultwarden/internal/config"
	"github.com/mfat/ezVaultwarden/internal/vault"
)

func (a *app) connect([]string) error {
	s, err := a.load()
	if err != nil {
		return err
	}
	email := "you@example.com"
	if emails, _ := vault.Accounts(a.paths.Data()); len(emails) > 0 {
		email = emails[0]
	}
	fmt.Fprintf(a.out, "Server URL: %s\n\n", s.URL())
	fmt.Fprintln(a.out, "Browser extension or desktop app")
	fmt.Fprintln(a.out, "  Settings > Self-hosted > paste the URL above")
	realCert := s.Mode != "local"
	if !realCert {
		fmt.Fprintf(a.out, "  Your browser will warn you until you import %s into its certificate settings\n", a.paths.RootCert())
	}
	fmt.Fprintln(a.out, "\nbw CLI")
	if !realCert {
		fmt.Fprintf(a.out, "  export NODE_EXTRA_CA_CERTS=%s\n", a.paths.RootCert())
	}
	fmt.Fprintf(a.out, "  bw config server %s\n", s.URL())
	fmt.Fprintf(a.out, "  bw login %s\n", email)
	fmt.Fprintln(a.out, "\nPhone")
	switch s.Mode {
	case "tailnet":
		fmt.Fprintln(a.out, "  Install Tailscale and Bitwarden, log in to Tailscale, then paste the URL above")
	case "lan":
		fmt.Fprintln(a.out, "  Join the same network, install Bitwarden, then paste the URL above")
	default:
		fmt.Fprintln(a.out, "  Not reachable in local mode. Try: ezvaultwarden expose tailnet")
	}
	return nil
}

func (a *app) backup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	auto := fs.Bool("auto", false, "scheduled run")
	target := fs.String("target", "", "also keep copies in this folder from now on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := a.load()
	if err != nil {
		return err
	}
	if *target != "" {
		dir, err := filepath.Abs(*target)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		s.BackupTargets = append(s.BackupTargets, dir)
		if err := config.Save(a.paths, s); err != nil {
			return err
		}
	}
	file, err := a.makeBackup(s)
	if err != nil {
		return err
	}
	a.ok("Backup saved: %s", file)
	if !*auto && len(s.BackupTargets) == 0 {
		a.warn("Backups are on this disk. Add a second location:")
		fmt.Fprintln(a.out, "  ezvaultwarden backup --target /mnt/usb")
	}
	return nil
}

// makeBackup pauses the server so the database is at rest. The server comes
// back whatever happens to the backup.
func (a *app) makeBackup(s config.State) (file string, err error) {
	wasRunning := a.rt.Running()
	if err := a.rt.StopVault(); err != nil {
		return "", err
	}
	defer func() {
		if wasRunning {
			if startErr := a.rt.StartVault(); startErr != nil && err == nil {
				err = startErr
			}
		}
	}()
	file, err = backup.Create(a.paths.Data(), a.paths.Backups(), s.BackupRecipient, a.now())
	if err != nil {
		return "", err
	}
	if err := backup.Prune(a.paths.Backups(), keepBackups); err != nil {
		return file, err
	}
	for _, dir := range s.BackupTargets {
		if err := copyFile(file, filepath.Join(dir, filepath.Base(file))); err != nil {
			return file, fmt.Errorf("copying to %s: %w", dir, err)
		}
		if err := backup.Prune(dir, keepBackups); err != nil {
			return file, err
		}
	}
	return file, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst+".partial", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(dst+".partial", dst)
}

func (a *app) restore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	file := fs.String("file", "", "backup to restore (default: choose from a list)")
	keyFile := fs.String("key-file", "", "read the backup key from this file")
	assume := fs.Bool("yes", false, "don't ask for confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := a.load()
	if err != nil {
		return err
	}
	if *file == "" {
		if *file, err = a.chooseBackup(); err != nil {
			return err
		}
	}
	var key string
	if *keyFile != "" {
		raw, err := os.ReadFile(*keyFile)
		if err != nil {
			return err
		}
		key = string(raw)
	} else if key, err = a.secret("Backup key: "); err != nil {
		return err
	}
	if !*assume {
		yes, err := a.confirm("This replaces your current vault. A safety copy is kept. Continue?", false)
		if err != nil {
			return err
		}
		if !yes {
			return errors.New("nothing changed")
		}
	}
	if err := a.rt.StopVault(); err != nil {
		return err
	}
	keep, restoreErr := backup.Restore(*file, key, a.paths.Data(), a.now())
	// Back up either way: a refused restore must not leave the vault down.
	if err := a.rt.StartVault(); err != nil {
		return err
	}
	if restoreErr != nil {
		return restoreErr
	}
	if err := a.health(context.Background(), s); err != nil {
		return err
	}
	a.ok("Restored and running. The previous vault is in %s", keep)
	a.warn("Apps that are logged in still show their old copy. Log out and back in on each one.")
	return nil
}

func (a *app) chooseBackup() (string, error) {
	files, err := backup.List(a.paths.Backups())
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", errors.New("no backups found here. Pass one with --file")
	}
	fmt.Fprintln(a.out, "Choose a backup:")
	for i, f := range files {
		size := int64(0)
		if info, err := os.Stat(f); err == nil {
			size = info.Size()
		}
		fmt.Fprintf(a.out, "  %d) %s  %d KB\n", i+1, filepath.Base(f), size/1024)
	}
	fmt.Fprint(a.out, "Choice: ")
	s, err := a.line()
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > len(files) {
		return "", errors.New("that isn't one of the choices")
	}
	return files[n-1], nil
}

const deletePhrase = "delete my vault"

func (a *app) uninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	deleteData := fs.Bool("delete-data", false, "also delete the vault and backups")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := a.load()
	if err != nil {
		return err
	}
	if *deleteData {
		fmt.Fprintf(a.out, "This permanently deletes your vault and every backup in %s.\n", a.paths.Root)
		fmt.Fprintf(a.out, "Type %q to confirm: ", deletePhrase)
		answer, err := a.line()
		if err != nil {
			return err
		}
		if !strings.EqualFold(answer, deletePhrase) {
			return errors.New("nothing changed")
		}
	}
	if s.Mode == "tailnet" {
		if err := a.ts.StopServing(context.Background()); err != nil {
			a.warn("Couldn't stop Tailscale serving the vault: %v", err)
		}
	}
	if err := a.forgetDNSToken(); err != nil {
		a.warn("Couldn't delete the DNS token at %s: %v", a.paths.DNSToken(), err)
	}
	if err := a.rt.Remove(); err != nil {
		return err
	}
	a.ok("Service removed")
	a.warnAboutOldAnchors()
	if *deleteData {
		if err := os.RemoveAll(a.paths.Root); err != nil {
			return err
		}
		a.ok("Vault deleted")
		return nil
	}
	if err := os.Remove(a.paths.State()); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Your vault and backups are still in %s\n", a.paths.Root)
	return nil
}
