package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/N11CE1/ynabUp/store"
)

func TestHealthzHandler_ReportsOKWithNoSyncYet(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer db.Close()

	server := httptest.NewServer(healthzHandler(db))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status=ok, got %v", body)
	}
	if _, has := body["last_synced_at"]; has {
		t.Fatalf("expected no last_synced_at with a fresh db, got %v", body)
	}
}

func TestHealthzHandler_ReportsLastSyncedAtOnceSet(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer db.Close()

	if err := store.SetSetting(db, "up_last_synced_at", "2026-03-14T09:30:00Z"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	server := httptest.NewServer(healthzHandler(db))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body["last_synced_at"] != "2026-03-14T09:30:00Z" {
		t.Fatalf("expected last_synced_at to round-trip, got %v", body)
	}
}
