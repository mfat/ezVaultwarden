// Package backup writes and restores age-encrypted archives of the data folder.
//
// Backups are encrypted to a public key, so the machine that makes them holds
// nothing that can read them: stealing the server does not expose old backups,
// and the daily timer never needs a secret.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

const suffix = ".tar.gz.age"

// NewKey returns a fresh backup key: the recipient stored on this machine and
// the secret only the user keeps.
func NewKey() (recipient, secret string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return id.Recipient().String(), id.String(), nil
}

// Create archives dataDir into dir and returns the new file's path. The file
// only takes its final name once it is complete.
func Create(dataDir, dir, recipient string, now time.Time) (string, error) {
	r, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return "", fmt.Errorf("backup key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".partial-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	enc, err := age.Encrypt(tmp, r)
	if err != nil {
		return "", err
	}
	gz := gzip.NewWriter(enc)
	if err := writeTar(gz, dataDir); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	final := uniquePath(dir, now)
	return final, os.Rename(tmp.Name(), final)
}

func uniquePath(dir string, now time.Time) string {
	base := filepath.Join(dir, "vault-"+now.Format("20060102-150405"))
	final := base + suffix
	for i := 1; ; i++ {
		if _, err := os.Stat(final); errors.Is(err, fs.ErrNotExist) {
			return final
		}
		final = fmt.Sprintf("%s-%d%s", base, i, suffix)
	}
}

func writeTar(w io.Writer, dir string) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// Restore decrypts file into a new folder next to dataDir and swaps it in. The
// data it replaces is kept and its path returned, so a restore is never the
// step that loses a vault.
func Restore(file, secret, dataDir string, now time.Time) (string, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(secret))
	if err != nil {
		return "", errors.New("that is not a backup key (it starts with AGE-SECRET-KEY-)")
	}
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	dec, err := age.Decrypt(f, id)
	if err != nil {
		return "", errors.New("this backup key does not open that backup")
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return "", fmt.Errorf("corrupt backup: %w", err)
	}
	defer gz.Close()

	parent := filepath.Dir(dataDir)
	staged, err := os.MkdirTemp(parent, ".restore-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	if err := extractTar(gz, staged); err != nil {
		return "", fmt.Errorf("corrupt backup: %w", err)
	}
	if _, err := os.Stat(filepath.Join(staged, "db.sqlite3")); err != nil {
		return "", errors.New("corrupt backup: no vault database inside")
	}
	keep := filepath.Join(parent, "data.before-restore-"+now.Format("20060102-150405"))
	if err := os.Rename(dataDir, keep); err != nil {
		return "", err
	}
	if err := os.Rename(staged, dataDir); err != nil {
		if undo := os.Rename(keep, dataDir); undo != nil {
			return "", fmt.Errorf("%w; your data is safe in %s", err, keep)
		}
		return "", err
	}
	return keep, nil
}

func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rel := filepath.FromSlash(hdr.Name)
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		p := filepath.Join(dest, rel)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected entry in archive: %q", hdr.Name)
		}
	}
}

// List returns backups in dir, newest first.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	return files, nil
}

// Prune removes all but the newest keep backups.
func Prune(dir string, keep int) error {
	files, err := List(dir)
	if err != nil || len(files) <= keep {
		return err
	}
	for _, f := range files[keep:] {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	return nil
}
