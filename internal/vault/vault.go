// Package vault reads the little ezVaultwarden needs to know about the vault:
// which accounts exist. It never touches vault contents.
package vault

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Accounts returns the registered email addresses. A vault that has not
// created its database yet simply has none.
func Accounts(dataDir string) ([]string, error) {
	db, err := open(dataDir)
	if db == nil || err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT email FROM users")
	if err != nil {
		return nil, fmt.Errorf("reading accounts: %w", err)
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}
	return emails, rows.Err()
}

// HasTwoStepLogin reports whether every account has a second login step. The
// types are authenticator app, email, Duo, YubiKey, U2F and WebAuthn. The table
// also holds temporary challenges, which must not count.
func HasTwoStepLogin(dataDir string) (bool, error) {
	db, err := open(dataDir)
	if db == nil || err != nil {
		return false, err
	}
	defer db.Close()
	var without int
	err = db.QueryRow(`SELECT COUNT(*) FROM users WHERE uuid NOT IN
		(SELECT user_uuid FROM twofactor WHERE enabled = 1 AND atype IN (0, 1, 2, 3, 4, 7))`).Scan(&without)
	if err != nil {
		return false, fmt.Errorf("reading two-step login: %w", err)
	}
	return without == 0, nil
}

// open returns nil if the vault has not created its database yet.
func open(dataDir string) (*sql.DB, error) {
	dbPath := filepath.Join(dataDir, "db.sqlite3")
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}).String()
	return sql.Open("sqlite", dsn)
}
