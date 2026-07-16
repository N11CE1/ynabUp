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

	"github.com/N11CE1/ynabUp.git/pipeline"
	"github.com/N11CE1/ynabUp.git/up"
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

// Handler verifies and processes a single Up webhook event. Only
// TRANSACTION_CREATED and TRANSACTION_SETTLED events carry a transaction to
// sync; everything else (e.g. PING, TRANSACTION_DELETED) is acknowledged
// and ignored.
func Handler(db *sql.DB, cfg pipeline.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		if !VerifySignature(body, r.Header.Get("X-Up-Authenticity-Signature"), cfg.WebhookSecret) {
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

		if txnRef == nil || (eventType != "TRANSACTION_CREATED" && eventType != "TRANSACTION_SETTLED") {
			log.Printf("ignoring webhook event: %s", eventType)
			w.WriteHeader(http.StatusOK)
			return
		}

		txn, err := up.FetchTransactionByID(txnRef.ID, cfg.UpToken)
		if err != nil {
			log.Printf("failed to fetch transaction %s: %v", txnRef.ID, err)
			http.Error(w, "failed to fetch transaction", http.StatusInternalServerError)
			return
		}

		skipped, err := pipeline.SyncTransaction(db, txn, cfg)
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
