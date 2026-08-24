package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/N11CE1/ynabUp/pipeline"
	"github.com/N11CE1/ynabUp/up"
)

type Event struct {
	Data struct {
		Attributes struct {
			EventType string `json:"eventType"`
		} `json:"attributes"`
		Relationships struct {
			Transaction struct {
				Data *struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"transaction"`
		} `json:"relationships"`
	} `json:"data"`
}

// VerifySignature checks Up's HMAC-SHA256 webhook signature against the raw
// request body, using constant-time comparison to avoid timing attacks.
func VerifySignature(payload []byte, signatureHeader, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)

	got, err := hex.DecodeString(signatureHeader)
	if err != nil {
		return false
	}

	return hmac.Equal(expected, got)
}

// Handler verifies and processes a single Up webhook event.
// TRANSACTION_CREATED and TRANSACTION_SETTLED carry a transaction to sync;
// TRANSACTION_DELETED retracts it from YNAB if we'd already synced it -
// Up sends this when e.g. an authorization hold (posted with a placeholder
// amount) is released in favour of a separate, later transaction carrying
// the real settled amount, rather than the hold itself updating in place.
// Everything else (e.g. PING) is acknowledged and ignored.
func Handler(db *sql.DB, cfg pipeline.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		if !VerifySignature(body, r.Header.Get("X-Up-Authenticity-Signature"), cfg.UpWebhookSecret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		var event Event
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "failed to parse event", http.StatusBadRequest)
			return
		}

		eventType := event.Data.Attributes.EventType
		txnRef := event.Data.Relationships.Transaction.Data

		if txnRef == nil || (eventType != "TRANSACTION_CREATED" && eventType != "TRANSACTION_SETTLED" && eventType != "TRANSACTION_DELETED") {
			log.Printf("ignoring webhook event: %s", eventType)
			w.WriteHeader(http.StatusOK)
			return
		}

		if eventType == "TRANSACTION_DELETED" {
			deleted, hadRecord, err := pipeline.DeleteSyncedUpTransaction(db, txnRef.ID, cfg)
			if err != nil {
				log.Printf("failed to delete synced transaction %s: %v", txnRef.ID, err)
				http.Error(w, "failed to delete synced transaction", http.StatusInternalServerError)
				return
			}
			switch {
			case deleted:
				log.Printf("retracted transaction %s from YNAB (deleted at source)", txnRef.ID)
			case hadRecord:
				log.Printf("transaction %s was deleted at source but has no known YNAB id (synced before delete-tracking, or via duplicate reconciliation) - needs manual cleanup", txnRef.ID)
			default:
				log.Printf("transaction %s was deleted at source but was never synced, nothing to do", txnRef.ID)
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		txn, err := up.FetchTransactionByID(txnRef.ID, cfg.UpToken)
		if err != nil {
			log.Printf("failed to fetch transaction %s: %v", txnRef.ID, err)
			http.Error(w, "failed to fetch transaction", http.StatusInternalServerError)
			return
		}

		skipped, err := pipeline.SyncUpTransaction(db, txn, cfg)
		if err != nil {
			log.Printf("failed to sync transaction %s: %v", txn.ID, err)
			http.Error(w, "failed to sync transaction", http.StatusInternalServerError)
			return
		}

		if skipped {
			log.Printf("transaction %s already synced, skipping", txn.ID)
		} else {
			log.Printf("synced transaction %s via webhook", txn.ID)
		}

		w.WriteHeader(http.StatusOK)
	}
}
