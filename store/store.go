package store

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

// Open opens (creating if needed) the SQLite file tracking which
// transactions have already been synced to YNAB.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS synced_transactions (
			import_id TEXT PRIMARY KEY,
			synced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, err
	}

	return db, nil
}

func IsSynced(db *sql.DB, importID string) (bool, error) {
	var exists bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM synced_transactions WHERE import_id = ?)`, importID).Scan(&exists)
	return exists, err
}

func MarkSynced(db *sql.DB, importID string) error {
	_, err := db.Exec(`INSERT INTO synced_transactions (import_id) VALUES (?)`, importID)
	return err
}
