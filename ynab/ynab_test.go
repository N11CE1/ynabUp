package ynab

import (
	"strings"
	"testing"
	"time"

	"github.com/N11CE1/ynabUp/up"
)

func TestSafeImportID_ShortIDUnchanged(t *testing.T) {
	id := "short-id-123"
	if got := SafeImportID(id); got != id {
		t.Fatalf("expected short id to pass through unchanged, got %q", got)
	}
}

func TestSafeImportID_ExactlyMaxLengthUnchanged(t *testing.T) {
	id := strings.Repeat("a", maxImportIDLength)
	if got := SafeImportID(id); got != id {
		t.Fatalf("expected a %d-char id to pass through unchanged, got %q", maxImportIDLength, got)
	}
}

func TestSafeImportID_OverLongIDIsHashedAndDeterministic(t *testing.T) {
	id := strings.Repeat("a", maxImportIDLength+1)

	got := SafeImportID(id)
	if len(got) != maxImportIDLength {
		t.Fatalf("expected hashed id to be %d chars, got %d (%q)", maxImportIDLength, len(got), got)
	}
	if got == id[:maxImportIDLength] {
		t.Fatal("expected a hash, not a truncation of the original id")
	}

	if again := SafeImportID(id); again != got {
		t.Fatalf("expected SafeImportID to be deterministic, got %q then %q", got, again)
	}
}

func TestSafeImportID_DifferentOverLongIDsDontCollide(t *testing.T) {
	a := strings.Repeat("a", maxImportIDLength+1)
	b := strings.Repeat("b", maxImportIDLength+1)

	if SafeImportID(a) == SafeImportID(b) {
		t.Fatal("expected different over-long ids to hash to different import ids")
	}
}

func TestTransform_UnsettledTransactionIsUncleared(t *testing.T) {
	var txn up.Transaction
	txn.ID = "txn-1"
	txn.Attributes.Description = "Coffee"
	txn.Attributes.Amount.ValueInBaseUnits = -550
	txn.Attributes.CreatedAt = time.Date(2026, 3, 14, 9, 30, 0, 0, time.UTC)
	txn.Attributes.SettledAt = nil

	got := Transform(txn, "acct-1")

	if got.Cleared != "uncleared" {
		t.Fatalf("expected uncleared for a nil SettledAt, got %q", got.Cleared)
	}
	if got.Amount != -5500 {
		t.Fatalf("expected cents converted to milliunits (-550 -> -5500), got %d", got.Amount)
	}
	if got.Date != "2026-03-14" {
		t.Fatalf("expected date formatted as 2026-03-14, got %q", got.Date)
	}
	if got.PayeeName != "Coffee" {
		t.Fatalf("expected payee name from description, got %q", got.PayeeName)
	}
	if got.AccountID != "acct-1" {
		t.Fatalf("expected the passed-in account id, got %q", got.AccountID)
	}
	if got.ImportID != "txn-1" {
		t.Fatalf("expected import id from SafeImportID(txn.ID), got %q", got.ImportID)
	}
}

func TestTransform_SettledTransactionIsCleared(t *testing.T) {
	settledAt := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)

	var txn up.Transaction
	txn.ID = "txn-2"
	txn.Attributes.Amount.ValueInBaseUnits = 1000
	txn.Attributes.CreatedAt = time.Date(2026, 3, 14, 9, 30, 0, 0, time.UTC)
	txn.Attributes.SettledAt = &settledAt

	got := Transform(txn, "acct-1")

	if got.Cleared != "cleared" {
		t.Fatalf("expected cleared for a non-nil SettledAt, got %q", got.Cleared)
	}
}
