package pipeline

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/N11CE1/ynabUp/store"
	"github.com/N11CE1/ynabUp/up"
	"github.com/N11CE1/ynabUp/ynab"
)

// Config bundles the credentials and IDs shared across sync entry points
// (one-shot sync, Up webhook), so they don't need to be threaded
// individually through every function.
type Config struct {
	UpToken         string
	UpWebhookSecret string
	// UpAccountMap maps an Up account ID to the YNAB account ID it syncs
	// into. Having more than one entry is what makes internal transfers
	// between two of the user's own Up accounts (e.g. Spending <-> Saver)
	// detectable via Up's transferAccount relationship.
	UpAccountMap map[string]string

	// YnabTransferPayeeIDs maps a YNAB account ID to that account's
	// transfer payee ID, needed to post a real linked transfer via
	// ynab.Transaction.PayeeID rather than a normal PayeeName transaction.
	YnabTransferPayeeIDs map[string]string

	// SavingsAccountIDs are YNAB account IDs that a linked transfer can land
	// in as its destination. YNAB transfers move cash between accounts but
	// never touch a category's assigned amount, so without this a transfer
	// into savings (e.g. Up's Round Up) would leave SavingsCategoryID never
	// reflecting money that's actually been set aside. Left unset, no
	// category funding happens.
	SavingsAccountIDs map[string]bool
	// SavingsCategoryID is the single YNAB category bumped by the amount of
	// any transfer landing in a SavingsAccountIDs account. Funded from
	// nowhere else - Ready to Assign goes negative by the same amount until
	// manually covered.
	SavingsCategoryID string

	BudgetID  string
	YnabToken string
}

// SyncTransaction checks whether a transaction has already been synced, and
// if not, posts it to YNAB and records it as synced. Callers transform their
// source-specific transaction into ynab.Transaction first, so this is shared
// by the one-shot batch sync and the Up webhook.
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
// to and syncs it to YNAB, handling three cases:
//
//  1. Not a transfer (no TransferAccount): posted as a normal transaction.
//
//  2. A real two-sided transfer (TransactionType "Transfer"): both accounts
//     have their own matching Up transaction record. Only the outflow side
//     posts, as a real linked YNAB transfer (via the destination account's
//     transfer payee); the inflow side is recorded as handled without being
//     posted, since YNAB creates it automatically.
//
//  3. A one-sided attribution (e.g. "Round Up", "Cover"): Up sets
//     TransferAccount for display purposes, but - unlike a real transfer -
//     creates no matching record on the other account. Only the inflow
//     record exists (e.g. the Saver gaining a round-up), and the money
//     that actually left the source account would otherwise never be
//     reflected in YNAB at all. Since this transaction already carries
//     everything needed (source via TransferAccount, amount, date), the
//     missing outflow is constructed from it directly and posted to the
//     source as a linked transfer, same mechanism as case 2.
func SyncUpTransaction(db *sql.DB, txn up.Transaction, cfg Config) (skipped bool, err error) {
	accountID, ok := cfg.UpAccountMap[txn.Relationships.Account.Data.ID]
	if !ok {
		return false, fmt.Errorf("no YNAB account mapped for up account %s", txn.Relationships.Account.Data.ID)
	}

	transferAccount := txn.Relationships.TransferAccount.Data
	if transferAccount == nil {
		return SyncTransaction(db, ynab.Transform(txn, accountID), cfg)
	}

	otherAccountID, tracked := cfg.UpAccountMap[transferAccount.ID]
	if !tracked {
		// The other side involves an Up account we're not syncing - no
		// YNAB account to link to, so treat it as a normal transaction.
		return SyncTransaction(db, ynab.Transform(txn, accountID), cfg)
	}

	if txn.Attributes.TransactionType == "Transfer" {
		if txn.Attributes.Amount.ValueInBaseUnits >= 0 {
			return suppressAsHandled(db, txn.ID)
		}
		return postAsLinkedTransfer(db, txn, accountID, otherAccountID, false, cfg)
	}

	if txn.Attributes.Amount.ValueInBaseUnits >= 0 {
		// One-sided inflow (Round Up, Cover, ...): construct the missing
		// outflow from this same transaction, posted to the source account
		// with the sign flipped, rather than letting the money vanish.
		return postAsLinkedTransfer(db, txn, otherAccountID, accountID, true, cfg)
	}

	// A negative amount with TransferAccount set but not type "Transfer" -
	// not a pattern seen in practice; fall back to a normal post rather
	// than guess at a linking direction.
	return SyncTransaction(db, ynab.Transform(txn, accountID), cfg)
}

// suppressAsHandled records upTxnID as synced without posting anything to
// YNAB - used for the inflow side of a real transfer, which YNAB creates
// automatically once the matching outflow side posts.
func suppressAsHandled(db *sql.DB, upTxnID string) (skipped bool, err error) {
	importID := ynab.SafeImportID(upTxnID)
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

// postAsLinkedTransfer builds a YNAB transaction from txn for accountID and
// posts it with PayeeID set to destinationAccountID's transfer payee,
// making YNAB create a real linked transfer. flipSign negates the amount
// first, needed when constructing a synthetic outflow on behalf of a
// one-sided inflow record (Up's Round Up gives us the inflow's positive
// amount; the outflow we're posting on its behalf needs the negative).
func postAsLinkedTransfer(db *sql.DB, txn up.Transaction, accountID, destinationAccountID string, flipSign bool, cfg Config) (skipped bool, err error) {
	transferPayeeID, ok := cfg.YnabTransferPayeeIDs[destinationAccountID]
	if !ok {
		return false, fmt.Errorf("no transfer_payee_id known for YNAB account %s", destinationAccountID)
	}

	ynabTxn := ynab.Transform(txn, accountID)
	if flipSign {
		ynabTxn.Amount = -ynabTxn.Amount
	}
	ynabTxn.PayeeName = ""
	ynabTxn.PayeeID = transferPayeeID

	skipped, err = SyncTransaction(db, ynabTxn, cfg)
	if err != nil || skipped {
		return skipped, err
	}

	if cfg.SavingsAccountIDs[destinationAccountID] && cfg.SavingsCategoryID != "" {
		amount := ynabTxn.Amount
		if amount < 0 {
			amount = -amount
		}
		if err := ynab.FundCategory(cfg.BudgetID, cfg.SavingsCategoryID, amount, cfg.YnabToken); err != nil {
			log.Printf("posted transfer %s but failed to fund savings category: %v", txn.ID, err)
		}
	}

	return skipped, nil
}

const upWatermarkKey = "up_last_synced_at"

// RunOneShotSync fetches Up transactions since the last successful run (or
// full history on the very first run) and syncs any that haven't already
// been synced to YNAB. The watermark only advances when nothing fails, so a
// partial failure gets retried from the same starting point next run rather
// than being skipped over. Returns an error instead of exiting the process,
// since this also runs from a long-running background loop in -serve mode.
func RunOneShotSync(db *sql.DB, cfg Config) error {
	watermark, hasWatermark, err := store.GetSetting(db, upWatermarkKey)
	if err != nil {
		return fmt.Errorf("failed to read sync watermark: %w", err)
	}

	var since *time.Time
	if hasWatermark {
		t, err := time.Parse(time.RFC3339, watermark)
		if err != nil {
			return fmt.Errorf("failed to parse stored watermark %q: %w", watermark, err)
		}
		since = &t
	}

	runStartedAt := time.Now()

	transactions, err := up.FetchTransactions(cfg.UpToken, since)
	if err != nil {
		return fmt.Errorf("failed to fetch transactions: %w", err)
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

	if failed == 0 {
		if err := store.SetSetting(db, upWatermarkKey, runStartedAt.Format(time.RFC3339)); err != nil {
			log.Printf("failed to update sync watermark: %v", err)
		}
	}

	fmt.Printf("Done: %d synced, %d already synced (skipped), %d failed\n", synced, skipped, failed)
	return nil
}
