package pipeline

import (
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/N11CE1/ynabUp/store"
	"github.com/N11CE1/ynabUp/up"
	"github.com/N11CE1/ynabUp/ynab"
)

// Config bundles the credentials and IDs shared across sync entry points
// (one-shot sync, Up webhook, BankSync webhook), so they don't need to be
// threaded individually through every function.
type Config struct {
	UpToken         string
	UpWebhookSecret string
	UpAccountID     string

	BankSyncAPIToken      string
	BankSyncWebhookSecret string
	// BankSyncAccountMap maps a BankSync accountId to the YNAB account ID
	// it should sync into, since BankSync may cover multiple bank accounts.
	BankSyncAccountMap map[string]string

	BudgetID  string
	YnabToken string
}

// SyncTransaction checks whether a transaction has already been synced, and
// if not, posts it to YNAB and records it as synced. Callers transform their
// source-specific transaction into ynab.Transaction first, so this is shared
// by the one-shot batch sync, the Up webhook, and the BankSync webhook.
func SyncTransaction(db *sql.DB, ynabTxn ynab.Transaction, cfg Config) (skipped bool, err error) {
	alreadySynced, err := store.IsSynced(db, ynabTxn.ImportID)
	if err != nil {
		return false, fmt.Errorf("checking sync state: %w", err)
	}
	if alreadySynced {
		return true, nil
	}

	wasDuplicate := false
	if err := ynab.PostTransaction(ynabTxn, cfg.BudgetID, cfg.YnabToken); err != nil {
		if !errors.Is(err, ynab.ErrDuplicateTransaction) {
			return false, fmt.Errorf("posting to YNAB: %w", err)
		}
		// Already in YNAB from before local state tracking existed (or a
		// prior run's markSynced failed) - reconcile rather than retry forever.
		wasDuplicate = true
	}

	if err := store.MarkSynced(db, ynabTxn.ImportID); err != nil {
		return false, fmt.Errorf("posted to YNAB but failed to record sync state: %w", err)
	}

	return wasDuplicate, nil
}

// RunOneShotSync fetches the current page of Up transactions and syncs any
// that haven't already been synced to YNAB.
func RunOneShotSync(db *sql.DB, cfg Config) {
	transactions, err := up.FetchTransactions(cfg.UpToken)
	if err != nil {
		log.Fatalf("failed to fetch transactions: %v", err)
	}

	if len(transactions) == 0 {
		log.Fatal("no transactions returned from Up")
	}

	synced, skipped, failed := 0, 0, 0

	for _, txn := range transactions {
		ynabTxn := ynab.Transform(txn, cfg.UpAccountID)
		wasSkipped, err := SyncTransaction(db, ynabTxn, cfg)
		if err != nil {
			log.Printf("failed to sync transaction %s: %v", txn.ID, err)
			failed++
			continue
		}
		if wasSkipped {
			skipped++
			continue
		}

		fmt.Printf("Synced -> ImportID: %s\n", txn.ID)
		synced++
	}

	fmt.Printf("Done: %d synced, %d already synced (skipped), %d failed\n", synced, skipped, failed)
}
