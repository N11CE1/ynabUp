package banksync

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/N11CE1/ynabUp.git/pipeline"
	"github.com/N11CE1/ynabUp.git/ynab"
)

const transactionsDeltaEvent = "transactions.delta"

type Event struct {
	ID          string        `json:"id"`
	Type        string        `json:"type"`
	APIVersion  string        `json:"apiVersion"`
	CreatedAt   string        `json:"createdAt"`
	WorkspaceID string        `json:"workspaceId"`
	Data        []Transaction `json:"data"`
}

type Transaction struct {
	ID                   string  `json:"id"`
	Date                 string  `json:"date"`
	Description          string  `json:"description"`
	MerchantName         string  `json:"merchantName"`
	Amount               float64 `json:"amount"`
	Currency             string  `json:"currency"`
	Category             string  `json:"category"`
	Type                 string  `json:"type"`
	Pending              bool    `json:"pending"`
	PendingTransactionID string  `json:"pendingTransactionId"`
	AccountID            string  `json:"accountId"`
	AccountName          string  `json:"accountName"`
}

// Transform converts a BankSync transaction row into YNAB's expected shape:
// whole-dollar amount -> milliunits, and pending -> YNAB's cleared status.
func Transform(txn Transaction, accountID string) ynab.Transaction {
	cleared := "cleared"
	if txn.Pending {
		cleared = "uncleared"
	}

	payee := txn.Description
	if txn.MerchantName != "" {
		payee = txn.MerchantName
	}

	return ynab.Transaction{
		AccountID: accountID,
		Date:      txn.Date,
		Amount:    int64(math.Round(txn.Amount * 1000)),
		PayeeName: payee,
		Cleared:   cleared,
		Approved:  false,
		ImportID:  txn.ID,
	}
}

// VerifySignature checks BankSync's Standard Webhooks signature: HMAC-SHA256
// over "{id}.{timestamp}.{body}", base64-encoded, keyed by the base64-decoded
// secret after stripping its "whsec_" prefix. The header may carry multiple
// space-separated "v1,<sig>" values during secret rotation - valid if any
// match. See https://www.standardwebhooks.com/.
func VerifySignature(id, timestamp string, body []byte, signatureHeader, secret string) bool {
	secret = strings.TrimPrefix(secret, "whsec_")
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return false
	}

	signedContent := fmt.Sprintf("%s.%s.%s", id, timestamp, body)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signedContent))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	for sig := range strings.FieldsSeq(signatureHeader) {
		candidate := sig
		if _, after, found := strings.Cut(sig, ","); found {
			candidate = after
		}
		if hmac.Equal([]byte(candidate), []byte(expected)) {
			return true
		}
	}

	return false
}

// verifyTimestamp rejects deliveries whose webhook-timestamp is more than 5
// minutes from now, guarding against replay of a captured signed request.
func verifyTimestamp(timestamp string) bool {
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}

	age := time.Since(time.Unix(seconds, 0))
	if age < 0 {
		age = -age
	}

	return age <= 5*time.Minute
}

// Handler verifies and processes a single BankSync webhook event. Only
// transactions.delta events carry transactions to sync; everything else
// (sync.completed, sync.failed, endpoint.test) is acknowledged and ignored.
func Handler(db *sql.DB, cfg pipeline.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		id := r.Header.Get("webhook-id")
		timestamp := r.Header.Get("webhook-timestamp")
		signature := r.Header.Get("webhook-signature")

		if !verifyTimestamp(timestamp) {
			http.Error(w, "stale timestamp", http.StatusUnauthorized)
			return
		}

		if !VerifySignature(id, timestamp, body, signature, cfg.BankSyncWebhookSecret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		var event Event
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "failed to parse event", http.StatusBadRequest)
			return
		}

		if event.Type != transactionsDeltaEvent {
			log.Printf("ignoring banksync event: %s", event.Type)
			w.WriteHeader(http.StatusOK)
			return
		}

		for _, txn := range event.Data {
			accountID, ok := cfg.BankSyncAccountMap[txn.AccountID]
			if !ok {
				log.Printf("no YNAB account mapped for banksync account %s, skipping transaction %s", txn.AccountID, txn.ID)
				continue
			}

			ynabTxn := Transform(txn, accountID)
			skipped, err := pipeline.SyncTransaction(db, ynabTxn, cfg)
			if err != nil {
				log.Printf("failed to sync transaction %s: %v", txn.ID, err)
				continue
			}

			if skipped {
				log.Printf("transaction %s already synced, skipping", txn.ID)
			} else {
				log.Printf("synced transaction %s via banksync webhook", txn.ID)
			}
		}

		w.WriteHeader(http.StatusOK)
	}
}
