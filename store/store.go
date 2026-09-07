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

	// ynab_transaction_id records YNAB's own ID for the posted transaction
	// (distinct from import_id), needed to delete it later if Up reports the
	// source transaction as deleted. Added after synced_transactions already
	// existed in production, so it's a migration rather than part of the
	// original CREATE TABLE. SQLite's ALTER TABLE has no ADD COLUMN IF NOT
	// EXISTS (unlike CREATE TABLE), so the column has to be checked for via
	// PRAGMA table_info first to make this safe to run on every startup.
	hasColumn, err := hasColumn(db, "synced_transactions", "ynab_transaction_id")
	if err != nil {
		return nil, err
	}
	if !hasColumn {
		if _, err := db.Exec(`ALTER TABLE synced_transactions ADD COLUMN ynab_transaction_id TEXT`); err != nil {
			return nil, err
		}
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)
	if err != nil {
		return nil, err
	}

	return db, nil
}

// hasColumn reports whether table has a column named column, via
// PRAGMA table_info - used to make ALTER TABLE ADD COLUMN migrations
// idempotent, since SQLite has no ADD COLUMN IF NOT EXISTS.
//
// table is concatenated directly into the query rather than passed as a
// parameter because SQLite's PRAGMA statements don't accept bind
// parameters at all - this is safe only because every caller passes a
// hardcoded literal (never user input); it would need revisiting if that
// ever changed.
func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}

	return false, rows.Err()
}

// GetSetting returns a stored value and whether it was present, e.g. a
// "last synced at" watermark used to bound a fetch instead of re-pulling
// full history every run.
func GetSetting(db *sql.DB, key string) (string, bool, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func SetSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func IsSynced(db *sql.DB, importID string) (bool, error) {
	var exists bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM synced_transactions WHERE import_id = ?)`, importID).Scan(&exists)
	return exists, err
}

// MarkSynced records importID as synced. ynabTransactionID is YNAB's own ID
// for the posted transaction, stored so it can be deleted later if needed;
// pass "" when it isn't known (e.g. YNAB reported the post as a duplicate,
// which doesn't disclose the existing transaction's ID).
//
// ON CONFLICT makes this idempotent rather than relying on every caller
// checking IsSynced first (true today, but not an invariant this function
// itself enforces) - a repeat call for an already-synced importID updates
// the recorded YNAB transaction ID instead of erroring, which also lets a
// later call that *does* learn the ID (e.g. after a duplicate-reconciliation
// post) self-heal a previously empty one.
func MarkSynced(db *sql.DB, importID, ynabTransactionID string) error {
	_, err := db.Exec(`
		INSERT INTO synced_transactions (import_id, ynab_transaction_id) VALUES (?, ?)
		ON CONFLICT(import_id) DO UPDATE SET ynab_transaction_id = excluded.ynab_transaction_id
	`, importID, ynabTransactionID)
	return err
}

// YnabTransactionID returns the YNAB transaction ID recorded for importID,
// and whether one was found and non-empty (it may be recorded-but-empty for
// transactions synced via the duplicate-reconciliation path, which never
// learns YNAB's ID for the existing transaction).
func YnabTransactionID(db *sql.DB, importID string) (string, bool, error) {
	var id sql.NullString
	err := db.QueryRow(`SELECT ynab_transaction_id FROM synced_transactions WHERE import_id = ?`, importID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id.String, id.Valid && id.String != "", nil
}

// DeleteSynced removes importID's sync record, e.g. after retracting the
// corresponding transaction from YNAB because Up reported the source
// transaction as deleted.
func DeleteSynced(db *sql.DB, importID string) error {
	_, err := db.Exec(`DELETE FROM synced_transactions WHERE import_id = ?`, importID)
	return err
}
