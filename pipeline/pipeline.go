package pipeline

import (
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/N11CE1/ynabUp.git/store"
	"github.com/N11CE1/ynabUp.git/up"
	"github.com/N11CE1/ynabUp.git/ynab"
)

// Config bundles the credentials and IDs shared by both the one-shot sync
// and the webhook server, so they don't need to be threaded individually
// through every function.
type Config struct {
	UpToken       string
	WebhookSecret string
	AccountID     string
	BudgetID      string
	YnabToken     string
}

// SyncTransaction checks whether a transaction has already been synced, and
// if not, transforms and posts it to YNAB and records it as synced. It's
// shared by both the one-shot batch sync and the webhook handler.
func SyncTransaction(db *sql.DB, txn up.Transaction, cfg Config) (skipped bool, err error) {
	alreadySynced, err := store.IsSynced(db, txn.ID)
	if err != nil {
		return false, fmt.Errorf("checking sync state: %w", err)
	}
	if alreadySynced {
		return true, nil
	}

	wasDuplicate := false
	ynabTxn := ynab.Transform(txn, cfg.AccountID)
	if err := ynab.PostTransaction(ynabTxn, cfg.BudgetID, cfg.YnabToken); err != nil {
		if !errors.Is(err, ynab.ErrDuplicateTransaction) {
			return false, fmt.Errorf("posting to YNAB: %w", err)
		}
		// Already in YNAB from before local state tracking existed (or a
		// prior run's markSynced failed) - reconcile rather than retry forever.
		wasDuplicate = true
	}

	if err := store.MarkSynced(db, txn.ID); err != nil {
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
		wasSkipped, err := SyncTransaction(db, txn, cfg)
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
