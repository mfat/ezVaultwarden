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
	dbPath := filepath.Join(dataDir, "db.sqlite3")
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
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
