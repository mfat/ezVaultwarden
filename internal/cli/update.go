package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"syscall"

	"github.com/mfat/ezVaultwarden/internal/backup"
	"github.com/mfat/ezVaultwarden/internal/config"
	"github.com/mfat/ezVaultwarden/internal/fetch"
)

// update has two stages because component versions are compiled in: a release
// replaces this program first, and the new program, which knows the new
// versions, upgrades the server.
func (a *app) update(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "only report what's available")
	assume := fs.Bool("yes", false, "don't ask for confirmation")
	apply := fs.Bool("apply", false, "upgrade the server to the versions in this build")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := a.load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if *apply {
		return a.applyUpdate(ctx, s)
	}
	base := fetch.ReleaseURL()
	latest, err := fetch.LatestVersion(ctx, base)
	if err != nil {
		return fmt.Errorf("couldn't check for updates: %w", err)
	}
	newer := fetch.Newer(latest, Version)
	if *check {
		switch {
		case newer:
			fmt.Fprintf(a.out, "Update available: ezVaultwarden %s → %s\nInstall it with: ezvaultwarden update\n", Version, latest)
		case a.serverIsCurrent():
			a.ok("Up to date: ezVaultwarden %s, Vaultwarden %s", Version, s.Version)
		default:
			fmt.Fprintln(a.out, "Vaultwarden has an update. Install it with: ezvaultwarden update")
		}
		return nil
	}
	if !newer && a.serverIsCurrent() {
		a.ok("Up to date: ezVaultwarden %s, Vaultwarden %s", Version, s.Version)
		return nil
	}
	if !*assume {
		if newer {
			fmt.Fprintf(a.out, "ezVaultwarden %s → %s\n", Version, latest)
		}
		yes, err := a.confirm("Stops the service for about 20 seconds. Continue?", true)
		if err != nil {
			return err
		}
		if !yes {
			return errors.New("nothing changed")
		}
	}
	if !newer {
		return a.applyUpdate(ctx, s)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "  Downloading ezVaultwarden...")
	if err := fetch.ReplaceBinary(ctx, base, goruntime.GOARCH, self, nil); err != nil {
		return err
	}
	a.ok("ezVaultwarden updated to %s", latest)
	return syscall.Exec(self, []string{self, "update", "--apply"}, os.Environ())
}

// serverIsCurrent reports whether the running components are the ones this
// build pins.
func (a *app) serverIsCurrent() bool {
	return a.currentID() == fetch.VersionID()
}

func (a *app) currentID() string {
	target, err := os.Readlink(a.paths.Current())
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// applyUpdate moves the server to this build's versions. Any failure past the
// switch puts everything back: the old binaries and the data as it was.
func (a *app) applyUpdate(ctx context.Context, s config.State) error {
	if a.serverIsCurrent() {
		a.ok("Vaultwarden %s is up to date", s.Version)
		return nil
	}
	previous := a.currentID()
	dir, err := a.fetchVersion(ctx)
	if err != nil {
		return err
	}
	a.ok("Downloaded and verified")

	wasRunning := a.rt.Running()
	if err := a.rt.Stop(); err != nil {
		return err
	}
	snapshot := a.paths.Snapshot()
	if err := os.RemoveAll(snapshot); err != nil {
		return err
	}
	if err := copyTree(a.paths.Data(), snapshot); err != nil {
		return fmt.Errorf("couldn't copy your data, so nothing was updated: %w", errors.Join(err, a.startIf(wasRunning)))
	}
	if _, err := backup.Create(a.paths.Data(), a.paths.Backups(), s.BackupRecipient, a.now()); err != nil {
		return fmt.Errorf("couldn't back up, so nothing was updated: %w", errors.Join(err, a.startIf(wasRunning)))
	}
	a.ok("Backup taken")

	// Saved before the switch, so an interrupted update can still be undone.
	s.Previous = &config.Previous{ID: previous, Version: s.Version}
	if err := config.Save(a.paths, s); err != nil {
		return errors.Join(err, a.startIf(wasRunning))
	}
	if err := a.setCurrent(dir); err != nil {
		return errors.Join(err, a.startIf(wasRunning))
	}
	s.Version = fetch.VaultwardenVersion
	if err := config.Save(a.paths, s); err != nil {
		return err
	}

	err = a.rt.Start()
	if err == nil {
		err = a.health(ctx, s)
	}
	if err != nil {
		fmt.Fprintf(a.out, "✗ Health check failed on %s\n", s.Version)
		if undo := a.undoUpdate(ctx, s); undo != nil {
			return fmt.Errorf("rolling back failed too: %w. Your data from before the update is in %s", undo, snapshot)
		}
		a.ok("Rolled back to %s, data restored from before the update", s.Previous.Version)
		return fmt.Errorf("the update failed, and your vault is running as it was. Details: ezvaultwarden logs (%v)", err)
	}
	a.ok("Updated and healthy: Vaultwarden %s", s.Version)
	return a.pruneVersions(previous)
}

func (a *app) startIf(wasRunning bool) error {
	if wasRunning {
		return a.rt.Start()
	}
	return nil
}

// undoUpdate returns to the previous binaries and data. A new server may have
// migrated the database, and the old one can't read that, so the binaries
// never go back without the data. What it replaces is kept beside the snapshot.
func (a *app) undoUpdate(ctx context.Context, s config.State) error {
	prev := s.Previous
	if err := a.rt.Stop(); err != nil {
		return err
	}
	if err := a.setCurrent(filepath.Join(a.paths.Versions(), prev.ID)); err != nil {
		return err
	}
	replaced := a.paths.Data() + ".replaced-" + a.now().Format("20060102-150405")
	if err := os.Rename(a.paths.Data(), replaced); err != nil {
		return err
	}
	if err := os.Rename(a.paths.Snapshot(), a.paths.Data()); err != nil {
		return errors.Join(err, os.Rename(replaced, a.paths.Data()))
	}
	s.Version, s.Previous = prev.Version, nil
	if err := config.Save(a.paths, s); err != nil {
		return err
	}
	if err := a.rt.Start(); err != nil {
		return err
	}
	return a.health(ctx, s)
}

func (a *app) rollback(args []string) error {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	assume := fs.Bool("yes", false, "don't ask for confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := a.load()
	if err != nil {
		return err
	}
	if s.Previous == nil {
		return errors.New("there's no earlier version to go back to")
	}
	if !*assume {
		fmt.Fprintf(a.out, "This returns to Vaultwarden %s and your data from just before that update.\n", s.Previous.Version)
		fmt.Fprintln(a.out, "Anything you've changed since then is set aside, not kept in the vault.")
		yes, err := a.confirm("Continue?", false)
		if err != nil {
			return err
		}
		if !yes {
			return errors.New("nothing changed")
		}
	}
	version := s.Previous.Version
	if err := a.undoUpdate(context.Background(), s); err != nil {
		return err
	}
	a.ok("Back on Vaultwarden %s", version)
	a.warn("Apps that are logged in still show their old copy. Log out and back in on each one.")
	return nil
}

// pruneVersions keeps the running version and the one rollback needs.
func (a *app) pruneVersions(keep string) error {
	entries, err := os.ReadDir(a.paths.Versions())
	if err != nil {
		return err
	}
	current := a.currentID()
	for _, e := range entries {
		if e.Name() == current || e.Name() == keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(a.paths.Versions(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
