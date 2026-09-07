package store

import (
	"database/sql"
	"testing"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestIsSynced_FalseForUnknownImportID(t *testing.T) {
	db := testDB(t)

	synced, err := IsSynced(db, "unknown")
	if err != nil {
		t.Fatalf("IsSynced: %v", err)
	}
	if synced {
		t.Fatal("expected an unknown import_id to report as not synced")
	}
}

func TestMarkSynced_ThenIsSyncedReturnsTrue(t *testing.T) {
	db := testDB(t)

	if err := MarkSynced(db, "txn-1", "ynab-1"); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	synced, err := IsSynced(db, "txn-1")
	if err != nil {
		t.Fatalf("IsSynced: %v", err)
	}
	if !synced {
		t.Fatal("expected txn-1 to report as synced after MarkSynced")
	}
}

func TestMarkSynced_IsIdempotentAndUpdatesYnabTransactionID(t *testing.T) {
	db := testDB(t)

	if err := MarkSynced(db, "txn-1", ""); err != nil {
		t.Fatalf("MarkSynced (first, no id known): %v", err)
	}

	// A later call learning the YNAB id shouldn't error just because the
	// import_id is already recorded, and should overwrite the empty id.
	if err := MarkSynced(db, "txn-1", "ynab-1"); err != nil {
		t.Fatalf("MarkSynced (second, id now known): %v", err)
	}

	id, has, err := YnabTransactionID(db, "txn-1")
	if err != nil {
		t.Fatalf("YnabTransactionID: %v", err)
	}
	if !has || id != "ynab-1" {
		t.Fatalf("expected the second MarkSynced call to update the recorded id, got %q (has=%v)", id, has)
	}
}

func TestYnabTransactionID_NotFoundForUnknownImportID(t *testing.T) {
	db := testDB(t)

	id, has, err := YnabTransactionID(db, "unknown")
	if err != nil {
		t.Fatalf("YnabTransactionID: %v", err)
	}
	if has || id != "" {
		t.Fatalf("expected not found for an unknown import_id, got id=%q has=%v", id, has)
	}
}

func TestYnabTransactionID_RecordedButEmptyReportsNotHas(t *testing.T) {
	db := testDB(t)

	// Simulates the duplicate-reconciliation path, which records synced
	// state without ever learning YNAB's own transaction id.
	if err := MarkSynced(db, "txn-1", ""); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	id, has, err := YnabTransactionID(db, "txn-1")
	if err != nil {
		t.Fatalf("YnabTransactionID: %v", err)
	}
	if has || id != "" {
		t.Fatalf("expected a recorded-but-empty id to report has=false, got id=%q has=%v", id, has)
	}
}

func TestDeleteSynced_RemovesRecordAndIsSyncedReturnsFalse(t *testing.T) {
	db := testDB(t)

	if err := MarkSynced(db, "txn-1", "ynab-1"); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}
	if err := DeleteSynced(db, "txn-1"); err != nil {
		t.Fatalf("DeleteSynced: %v", err)
	}

	synced, err := IsSynced(db, "txn-1")
	if err != nil {
		t.Fatalf("IsSynced: %v", err)
	}
	if synced {
		t.Fatal("expected txn-1 to report as not synced after DeleteSynced")
	}
}

func TestGetSetting_NotFoundForUnknownKey(t *testing.T) {
	db := testDB(t)

	_, has, err := GetSetting(db, "unknown")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if has {
		t.Fatal("expected an unknown key to report has=false")
	}
}

func TestSetSetting_ThenGetSettingRoundTrips(t *testing.T) {
	db := testDB(t)

	if err := SetSetting(db, "key1", "value1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	value, has, err := GetSetting(db, "key1")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if !has || value != "value1" {
		t.Fatalf("expected value1, got %q (has=%v)", value, has)
	}
}

func TestSetSetting_OverwritesExistingValue(t *testing.T) {
	db := testDB(t)

	if err := SetSetting(db, "key1", "value1"); err != nil {
		t.Fatalf("SetSetting (first): %v", err)
	}
	if err := SetSetting(db, "key1", "value2"); err != nil {
		t.Fatalf("SetSetting (second): %v", err)
	}

	value, _, err := GetSetting(db, "key1")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if value != "value2" {
		t.Fatalf("expected the second SetSetting to overwrite the first, got %q", value)
	}
}
