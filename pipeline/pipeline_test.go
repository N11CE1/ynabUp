package pipeline

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/N11CE1/ynabUp/store"
	"github.com/N11CE1/ynabUp/up"
	"github.com/N11CE1/ynabUp/ynab"
)

// testDB opens a fresh in-memory sync-state database for a single test.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

type recordedRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

// fakeYNAB stands in for the real YNAB API: it accepts transaction posts
// (rejecting any import_id listed in duplicateImportIDs with YNAB's 409) and
// category reads/writes, and records every request so tests can assert on
// what was actually sent.
type fakeYNAB struct {
	t                  *testing.T
	server             *httptest.Server
	duplicateImportIDs map[string]bool
	failImportIDs      map[string]bool
	categoryBudgeted   int64
	requests           []recordedRequest
}

// fakeTransactionID deterministically derives a YNAB transaction ID from an
// import_id, so tests can assert DeleteTransaction was called with the
// right ID without needing to inspect fakeYNAB's internal state.
func fakeTransactionID(importID string) string {
	return "ynab-" + importID
}

func newFakeYNAB(t *testing.T) *fakeYNAB {
	t.Helper()
	f := &fakeYNAB{t: t, duplicateImportIDs: map[string]bool{}, failImportIDs: map[string]bool{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))

	original := ynab.APIBaseURL
	ynab.APIBaseURL = f.server.URL
	t.Cleanup(func() {
		f.server.Close()
		ynab.APIBaseURL = original
	})

	return f
}

func (f *fakeYNAB) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var parsed map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &parsed); err != nil {
			f.t.Fatalf("fake YNAB: bad request body: %v", err)
		}
	}
	f.requests = append(f.requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Body: parsed})

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transactions"):
		txn, _ := parsed["transaction"].(map[string]any)
		importID, _ := txn["import_id"].(string)
		if f.duplicateImportIDs[importID] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if f.failImportIDs[importID] {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"transaction": map[string]any{"id": fakeTransactionID(importID)},
			},
		})

	case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/transactions/"):
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/transactions/"):
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/categories/"):
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"category": map[string]any{"id": "cat1", "budgeted": f.categoryBudgeted},
			},
		})

	case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/categories/"):
		var update struct {
			Category struct {
				Budgeted int64 `json:"budgeted"`
			} `json:"category"`
		}
		json.Unmarshal(body, &update)
		f.categoryBudgeted = update.Category.Budgeted
		w.WriteHeader(http.StatusOK)

	default:
		f.t.Fatalf("fake YNAB: unexpected request %s %s", r.Method, r.URL.Path)
	}
}

func (f *fakeYNAB) postCount() int {
	n := 0
	for _, r := range f.requests {
		if r.Method == http.MethodPost {
			n++
		}
	}
	return n
}

// deletedTransactionPaths returns the URL path of every DELETE request the
// fake YNAB server received, in order.
func (f *fakeYNAB) deletedTransactionPaths() []string {
	var paths []string
	for _, r := range f.requests {
		if r.Method == http.MethodDelete {
			paths = append(paths, r.Path)
		}
	}
	return paths
}

// patchedTransactionPaths returns the URL path of every PATCH request against
// a /transactions/ endpoint the fake YNAB server received, in order -
// distinct from category PATCHes, which hit a different path.
func (f *fakeYNAB) patchedTransactionPaths() []string {
	var paths []string
	for _, r := range f.requests {
		if r.Method == http.MethodPatch && strings.Contains(r.Path, "/transactions/") {
			paths = append(paths, r.Path)
		}
	}
	return paths
}

func baseCfg() Config {
	return Config{BudgetID: "budget1", YnabToken: "token"}
}

func TestSyncTransaction_PostsAndMarksSynced(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	txn := ynab.Transaction{AccountID: "acct1", ImportID: "txn-1", Amount: -5000, PayeeName: "Coffee"}
	skipped, err := SyncTransaction(db, txn, baseCfg())
	if err != nil {
		t.Fatalf("SyncTransaction: %v", err)
	}
	if skipped {
		t.Fatal("expected skipped=false for a new transaction")
	}
	if f.postCount() != 1 {
		t.Fatalf("expected 1 POST to YNAB, got %d", f.postCount())
	}

	synced, err := store.IsSynced(db, "txn-1")
	if err != nil {
		t.Fatalf("IsSynced: %v", err)
	}
	if !synced {
		t.Fatal("expected transaction to be recorded as synced")
	}

	ynabID, hasID, err := store.YnabTransactionID(db, "txn-1")
	if err != nil {
		t.Fatalf("YnabTransactionID: %v", err)
	}
	if !hasID || ynabID != fakeTransactionID("txn-1") {
		t.Fatalf("expected the YNAB transaction id from the post response to be recorded, got %q (hasID=%v)", ynabID, hasID)
	}
}

func TestSyncTransaction_AlreadySyncedSkipsWithoutPosting(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	if err := store.MarkSynced(db, "txn-1", "ynab-txn-1"); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	txn := ynab.Transaction{AccountID: "acct1", ImportID: "txn-1"}
	skipped, err := SyncTransaction(db, txn, baseCfg())
	if err != nil {
		t.Fatalf("SyncTransaction: %v", err)
	}
	if !skipped {
		t.Fatal("expected skipped=true for an already-synced transaction")
	}
	if f.postCount() != 0 {
		t.Fatalf("expected no POST to YNAB for an already-synced transaction, got %d", f.postCount())
	}
}

func TestSyncTransaction_AlreadySyncedButNowClearedUpdatesYNAB(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	if err := store.MarkSynced(db, "txn-1", "ynab-txn-1"); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	// Simulates Up settling a transaction that was originally synced while
	// still HELD/uncleared - same import_id, now reporting cleared.
	txn := ynab.Transaction{AccountID: "acct1", ImportID: "txn-1", Cleared: "cleared"}
	skipped, err := SyncTransaction(db, txn, baseCfg())
	if err != nil {
		t.Fatalf("SyncTransaction: %v", err)
	}
	if !skipped {
		t.Fatal("expected skipped=true for an already-synced transaction")
	}
	if f.postCount() != 0 {
		t.Fatalf("expected no POST to YNAB for an already-synced transaction, got %d", f.postCount())
	}

	paths := f.patchedTransactionPaths()
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/transactions/ynab-txn-1") {
		t.Fatalf("expected exactly one PATCH to /transactions/ynab-txn-1, got %v", paths)
	}
}

func TestSyncTransaction_AlreadySyncedWithNoKnownYNABIDSkipsClearedUpdate(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	// Simulates a transaction synced via the duplicate-reconciliation path,
	// which never learns YNAB's own ID for the existing transaction.
	if err := store.MarkSynced(db, "txn-1", ""); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	txn := ynab.Transaction{AccountID: "acct1", ImportID: "txn-1", Cleared: "cleared"}
	skipped, err := SyncTransaction(db, txn, baseCfg())
	if err != nil {
		t.Fatalf("SyncTransaction: %v", err)
	}
	if !skipped {
		t.Fatal("expected skipped=true for an already-synced transaction")
	}
	if len(f.patchedTransactionPaths()) != 0 {
		t.Fatal("expected no PATCH attempt when no YNAB transaction id is known")
	}
}

func TestSyncTransaction_DuplicateFromYNABMarksSyncedAndReturnsSkipped(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)
	f.duplicateImportIDs["txn-1"] = true

	txn := ynab.Transaction{AccountID: "acct1", ImportID: "txn-1"}
	skipped, err := SyncTransaction(db, txn, baseCfg())
	if err != nil {
		t.Fatalf("SyncTransaction: %v", err)
	}
	if !skipped {
		t.Fatal("expected skipped=true when YNAB reports a duplicate import_id")
	}

	synced, err := store.IsSynced(db, "txn-1")
	if err != nil {
		t.Fatalf("IsSynced: %v", err)
	}
	if !synced {
		t.Fatal("expected duplicate to still be reconciled into local sync state")
	}
}

func TestSyncTransaction_PostErrorDoesNotMarkSynced(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)
	f.failImportIDs["txn-1"] = true

	txn := ynab.Transaction{AccountID: "acct1", ImportID: "txn-1"}
	_, err := SyncTransaction(db, txn, baseCfg())
	if err == nil {
		t.Fatal("expected an error when YNAB rejects the post")
	}

	synced, err := store.IsSynced(db, "txn-1")
	if err != nil {
		t.Fatalf("IsSynced: %v", err)
	}
	if synced {
		t.Fatal("expected a failed post to NOT be recorded as synced")
	}
}

// upTxn builds an Up transaction for the transfer-linking tests below.
func upTxn(id, accountID string, amountBaseUnits int64, transactionType string, transferAccountID string) up.Transaction {
	var txn up.Transaction
	txn.ID = id
	txn.Attributes.Description = "Test txn"
	txn.Attributes.Amount.ValueInBaseUnits = amountBaseUnits
	txn.Attributes.CreatedAt = time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	txn.Attributes.TransactionType = transactionType
	txn.Relationships.Account.Data.ID = accountID
	if transferAccountID != "" {
		txn.Relationships.TransferAccount.Data = &struct {
			ID string `json:"id"`
		}{ID: transferAccountID}
	}
	return txn
}

func transferCfg() Config {
	cfg := baseCfg()
	cfg.UpAccountMap = map[string]string{
		"up-spending": "ynab-spending",
		"up-saver":    "ynab-saver",
	}
	cfg.YnabTransferPayeeIDs = map[string]string{
		"ynab-spending": "payee-spending",
		"ynab-saver":    "payee-saver",
	}
	return cfg
}

func TestSyncUpTransaction_NormalPostWhenNoTransferAccount(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	txn := upTxn("txn-1", "up-spending", -1000, "", "")
	skipped, err := SyncUpTransaction(db, txn, transferCfg())
	if err != nil {
		t.Fatalf("SyncUpTransaction: %v", err)
	}
	if skipped {
		t.Fatal("expected skipped=false")
	}
	if len(f.requests) != 1 || f.requests[0].Method != http.MethodPost {
		t.Fatalf("expected exactly 1 POST, got %+v", f.requests)
	}
	body := f.requests[0].Body["transaction"].(map[string]any)
	if body["payee_id"] != nil && body["payee_id"] != "" {
		t.Fatalf("expected a normal post with no payee_id, got %v", body["payee_id"])
	}
}

func TestSyncUpTransaction_UntrackedOtherAccountPostsNormal(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	// transferAccount points at an Up account not in UpAccountMap.
	txn := upTxn("txn-1", "up-spending", -1000, "Transfer", "up-untracked")
	skipped, err := SyncUpTransaction(db, txn, transferCfg())
	if err != nil {
		t.Fatalf("SyncUpTransaction: %v", err)
	}
	if skipped {
		t.Fatal("expected skipped=false")
	}
	if f.postCount() != 1 {
		t.Fatalf("expected 1 normal POST, got %d", f.postCount())
	}
}

func TestSyncUpTransaction_RealTransferOutflowPostsLinkedTransfer(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	txn := upTxn("txn-1", "up-spending", -2000, "Transfer", "up-saver")
	skipped, err := SyncUpTransaction(db, txn, transferCfg())
	if err != nil {
		t.Fatalf("SyncUpTransaction: %v", err)
	}
	if skipped {
		t.Fatal("expected skipped=false for the outflow side")
	}
	if f.postCount() != 1 {
		t.Fatalf("expected 1 POST for the outflow side, got %d", f.postCount())
	}

	body := f.requests[0].Body["transaction"].(map[string]any)
	if body["payee_id"] != "payee-saver" {
		t.Fatalf("expected linked transfer to destination's transfer payee, got %v", body["payee_id"])
	}
	if body["account_id"] != "ynab-spending" {
		t.Fatalf("expected outflow posted to the source account, got %v", body["account_id"])
	}
}

func TestSyncUpTransaction_RealTransferInflowSuppressed(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	txn := upTxn("txn-2", "up-saver", 2000, "Transfer", "up-spending")
	skipped, err := SyncUpTransaction(db, txn, transferCfg())
	if err != nil {
		t.Fatalf("SyncUpTransaction: %v", err)
	}
	if !skipped {
		t.Fatal("expected skipped=true for the inflow side of a real transfer")
	}
	if f.postCount() != 0 {
		t.Fatalf("expected no POST for the inflow side (YNAB creates it automatically), got %d", f.postCount())
	}

	synced, err := store.IsSynced(db, ynab.SafeImportID("txn-2"))
	if err != nil {
		t.Fatalf("IsSynced: %v", err)
	}
	if !synced {
		t.Fatal("expected the inflow side to be recorded as handled")
	}
}

// TestSyncUpTransaction_RoundUpConstructsSyntheticOutflow guards against the
// exact regression documented in HLSD.md: a Round Up-style one-sided inflow
// (transferAccount set, transactionType != "Transfer") must not be posted as
// a normal unlinked transaction, and must not vanish - it needs a synthetic
// linked outflow constructed on the source account's behalf.
func TestSyncUpTransaction_RoundUpConstructsSyntheticOutflow(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	cfg := transferCfg()
	cfg.SavingsAccountIDs = map[string]bool{"ynab-saver": true}
	cfg.SavingsCategoryID = "cat1"

	// The Saver's inflow record: transferAccount points at Spending, but
	// transactionType is not "Transfer" - Up's one-sided attribution shape.
	txn := upTxn("txn-3", "up-saver", 500, "", "up-spending")
	skipped, err := SyncUpTransaction(db, txn, cfg)
	if err != nil {
		t.Fatalf("SyncUpTransaction: %v", err)
	}
	if skipped {
		t.Fatal("expected skipped=false: the synthetic outflow must actually post")
	}
	if f.postCount() != 1 {
		t.Fatalf("expected exactly 1 synthetic outflow POST, got %d", f.postCount())
	}

	var postBody map[string]any
	for _, r := range f.requests {
		if r.Method == http.MethodPost {
			postBody = r.Body["transaction"].(map[string]any)
		}
	}
	if postBody["account_id"] != "ynab-spending" {
		t.Fatalf("expected synthetic outflow posted to the source (Spending) account, got %v", postBody["account_id"])
	}
	if postBody["payee_id"] != "payee-saver" {
		t.Fatalf("expected linked transfer to the Saver's transfer payee, got %v", postBody["payee_id"])
	}
	amount, _ := postBody["amount"].(float64)
	if amount >= 0 {
		t.Fatalf("expected the synthetic outflow amount to be negative (sign flipped), got %v", amount)
	}

	if f.categoryBudgeted != 5000 {
		t.Fatalf("expected savings category funded by the transfer amount (5000 milliunits), got %d", f.categoryBudgeted)
	}
}

func TestSyncUpTransaction_UnrecognisedTransferShapeFallsBackToNormalPost(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	// transferAccount set, not type "Transfer", negative amount - not a
	// pattern seen in practice; must not be dropped or mis-linked.
	txn := upTxn("txn-4", "up-spending", -300, "", "up-saver")
	skipped, err := SyncUpTransaction(db, txn, transferCfg())
	if err != nil {
		t.Fatalf("SyncUpTransaction: %v", err)
	}
	if skipped {
		t.Fatal("expected skipped=false")
	}
	if f.postCount() != 1 {
		t.Fatalf("expected exactly 1 normal POST, got %d", f.postCount())
	}
	body := f.requests[0].Body["transaction"].(map[string]any)
	if body["payee_id"] != nil && body["payee_id"] != "" {
		t.Fatalf("expected a normal unlinked post, got payee_id=%v", body["payee_id"])
	}
}

// TestDeleteSyncedUpTransaction_RetractsFromYNAB guards against the exact
// regression that motivated this function: Up releasing a placeholder
// authorization hold (e.g. a $1 transit tap-on) in favour of a separate,
// later transaction for the real settled amount, rather than the hold
// updating in place. Up reports the hold as TRANSACTION_DELETED, and
// without this, the placeholder would sit in YNAB forever alongside the
// real transaction.
func TestDeleteSyncedUpTransaction_RetractsFromYNAB(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	txn := ynab.Transaction{AccountID: "acct1", ImportID: "up-txn-1", Amount: -1000, PayeeName: "Transport for NSW"}
	if _, err := SyncTransaction(db, txn, baseCfg()); err != nil {
		t.Fatalf("SyncTransaction: %v", err)
	}

	deleted, hadRecord, err := DeleteSyncedUpTransaction(db, "up-txn-1", baseCfg())
	if err != nil {
		t.Fatalf("DeleteSyncedUpTransaction: %v", err)
	}
	if !deleted || !hadRecord {
		t.Fatalf("expected deleted=true, hadRecord=true, got deleted=%v hadRecord=%v", deleted, hadRecord)
	}

	wantPath := "/budgets/budget1/transactions/" + fakeTransactionID("up-txn-1")
	paths := f.deletedTransactionPaths()
	if len(paths) != 1 || paths[0] != wantPath {
		t.Fatalf("expected a single DELETE to %s, got %v", wantPath, paths)
	}

	synced, err := store.IsSynced(db, "up-txn-1")
	if err != nil {
		t.Fatalf("IsSynced: %v", err)
	}
	if synced {
		t.Fatal("expected the local sync record to be cleared after deletion")
	}
}

func TestDeleteSyncedUpTransaction_NeverSyncedIsNoOp(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	deleted, hadRecord, err := DeleteSyncedUpTransaction(db, "never-synced", baseCfg())
	if err != nil {
		t.Fatalf("DeleteSyncedUpTransaction: %v", err)
	}
	if deleted || hadRecord {
		t.Fatalf("expected deleted=false, hadRecord=false for a never-synced transaction, got deleted=%v hadRecord=%v", deleted, hadRecord)
	}
	if len(f.requests) != 0 {
		t.Fatalf("expected no requests to YNAB, got %+v", f.requests)
	}
}

// TestDeleteSyncedUpTransaction_NoKnownYnabIDLeavesRecord covers records
// synced before ynab_transaction_id was tracked, or via the duplicate-
// reconciliation path (YNAB's 409 doesn't disclose the existing
// transaction's ID) - these can't be auto-deleted and are left for manual
// cleanup, matching the pre-existing behaviour for that case.
func TestDeleteSyncedUpTransaction_NoKnownYnabIDLeavesRecord(t *testing.T) {
	db := testDB(t)
	f := newFakeYNAB(t)

	importID := ynab.SafeImportID("up-txn-legacy")
	if err := store.MarkSynced(db, importID, ""); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	deleted, hadRecord, err := DeleteSyncedUpTransaction(db, "up-txn-legacy", baseCfg())
	if err != nil {
		t.Fatalf("DeleteSyncedUpTransaction: %v", err)
	}
	if deleted || !hadRecord {
		t.Fatalf("expected deleted=false, hadRecord=true, got deleted=%v hadRecord=%v", deleted, hadRecord)
	}
	if len(f.requests) != 0 {
		t.Fatalf("expected no requests to YNAB when no YNAB id is known, got %+v", f.requests)
	}

	synced, err := store.IsSynced(db, importID)
	if err != nil {
		t.Fatalf("IsSynced: %v", err)
	}
	if !synced {
		t.Fatal("expected the local record to remain (needs manual cleanup) since it couldn't be auto-deleted")
	}
}
