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
	// UpAccountMap maps an Up account ID to the YNAB account ID it syncs
	// into. Having more than one entry is what makes internal transfers
	// between two of the user's own Up accounts (e.g. Spending <-> Saver)
	// detectable via Up's transferAccount relationship.
	UpAccountMap map[string]string

	BankSyncAPIToken      string
	BankSyncWebhookSecret string
	// BankSyncAccountMap maps a BankSync accountId to the YNAB account ID
	// it should sync into, since BankSync may cover multiple bank accounts.
	BankSyncAccountMap map[string]string

	// YnabTransferPayeeIDs maps a YNAB account ID to that account's
	// transfer payee ID, needed to post a real linked transfer via
	// ynab.Transaction.PayeeID rather than a normal PayeeName transaction.
	YnabTransferPayeeIDs map[string]string

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

// SyncUpTransaction resolves which mapped Up account a transaction belongs
// to and syncs it to YNAB. If the transaction is an internal transfer (per
// Up's transferAccount relationship) between two mapped Up accounts, it's
// posted as a real linked YNAB transfer instead of a normal transaction:
// only the outflow side is posted (using the destination account's transfer
// payee), since YNAB creates the inflow side automatically. The inflow side
// is recorded as handled without being posted, so it isn't reprocessed.
func SyncUpTransaction(db *sql.DB, txn up.Transaction, cfg Config) (skipped bool, err error) {
	accountID, ok := cfg.UpAccountMap[txn.Relationships.Account.Data.ID]
	if !ok {
		return false, fmt.Errorf("no YNAB account mapped for up account %s", txn.Relationships.Account.Data.ID)
	}

	transferAccount := txn.Relationships.TransferAccount.Data
	if transferAccount == nil {
		return SyncTransaction(db, ynab.Transform(txn, accountID), cfg)
	}

	destinationAccountID, tracked := cfg.UpAccountMap[transferAccount.ID]
	if !tracked {
		// Transfer involves an Up account we're not syncing - there's no
		// YNAB account to link it to, so treat it as a normal transaction.
		return SyncTransaction(db, ynab.Transform(txn, accountID), cfg)
	}

	if txn.Attributes.Amount.ValueInBaseUnits >= 0 {
		// The inflow side of a transfer between two mapped accounts - YNAB
		// creates this automatically once the outflow side posts, so just
		// record it as handled rather than posting it independently.
		importID := ynab.SafeImportID(txn.ID)
		alreadySynced, err := store.IsSynced(db, importID)
		if err != nil {
			return false, fmt.Errorf("checking sync state: %w", err)
		}
		if !alreadySynced {
			if err := store.MarkSynced(db, importID); err != nil {
				return false, fmt.Errorf("recording transfer inflow as handled: %w", err)
			}
		}
		return true, nil
	}

	transferPayeeID, ok := cfg.YnabTransferPayeeIDs[destinationAccountID]
	if !ok {
		return false, fmt.Errorf("no transfer_payee_id known for YNAB account %s", destinationAccountID)
	}

	ynabTxn := ynab.Transform(txn, accountID)
	ynabTxn.PayeeName = ""
	ynabTxn.PayeeID = transferPayeeID

	return SyncTransaction(db, ynabTxn, cfg)
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
		wasSkipped, err := SyncUpTransaction(db, txn, cfg)
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
