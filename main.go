package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"
)

const (
	APIBaseURL     = "https://api.up.com.au/api/v1/transactions"
	YNABAPIBaseURL = "https://api.ynab.com/v1"
	DefaultDBPath  = "sync.db"
	DefaultPort    = "8080"
)

type TransactionResponse struct {
	Data []Transaction `json:"data"`
}

type SingleTransactionResponse struct {
	Data Transaction `json:"data"`
}

type WebhookEvent struct {
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

type Transaction struct {
	ID         string `json:"id"`
	Attributes struct {
		Description string `json:"description"`
		Amount      struct {
			ValueInBaseUnits int64 `json:"valueInBaseUnits"`
		} `json:"amount"`
		CreatedAt time.Time  `json:"createdAt"`
		SettledAt *time.Time `json:"settledAt"`
	} `json:"attributes"`
}

type YNABTransaction struct {
	AccountID string `json:"account_id"`
	Date      string `json:"date"`
	Amount    int64  `json:"amount"`
	PayeeName string `json:"payee_name"`
	Cleared   string `json:"cleared"`
	Approved  bool   `json:"approved"`
	ImportID  string `json:"import_id"`
}

type YNABTransactionRequest struct {
	Transaction YNABTransaction `json:"transaction"`
}

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

// fetchTransactions retrieves the most recent page of transactions from Up.
func fetchTransactions(token string) ([]Transaction, error) {
	req, err := http.NewRequest("GET", APIBaseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	var result TransactionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Data, nil
}

// fetchTransactionByID retrieves a single transaction, used by the webhook
// handler since Up's webhook payload only contains a transaction ID.
func fetchTransactionByID(id, token string) (Transaction, error) {
	url := fmt.Sprintf("%s/%s", APIBaseURL, id)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return Transaction{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return Transaction{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Transaction{}, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	var result SingleTransactionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Transaction{}, err
	}

	return result.Data, nil
}

// ErrDuplicateTransaction indicates YNAB already has a transaction with this
// import_id on the account, e.g. from a sync predating local state tracking.
var ErrDuplicateTransaction = errors.New("transaction already exists in YNAB")

// postTransaction sends a single transformed transaction to YNAB's API.
func postTransaction(txn YNABTransaction, budgetID, token string) error {
	body, err := json.Marshal(YNABTransactionRequest{Transaction: txn})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/budgets/%s/transactions", YNABAPIBaseURL, budgetID)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return ErrDuplicateTransaction
	}

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status: %s: %s", resp.Status, respBody)
	}

	return nil
}

// transformTransaction converts an Up transaction into YNAB's expected shape:
// cents -> milliunits, and Up's settledAt presence -> YNAB's cleared status.
func transformTransaction(txn Transaction, accountID string) YNABTransaction {
	cleared := "uncleared"
	if txn.Attributes.SettledAt != nil {
		cleared = "cleared"
	}

	return YNABTransaction{
		AccountID: accountID,
		Date:      txn.Attributes.CreatedAt.Format("2006-01-02"),
		Amount:    txn.Attributes.Amount.ValueInBaseUnits * 10,
		PayeeName: txn.Attributes.Description,
		Cleared:   cleared,
		Approved:  false,
		ImportID:  txn.ID,
	}
}

// initDB opens (creating if needed) the SQLite file tracking which
// transactions have already been synced to YNAB.
func initDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS synced_transactions (
			import_id TEXT PRIMARY KEY,
			synced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, err
	}

	return db, nil
}

func isSynced(db *sql.DB, importID string) (bool, error) {
	var exists bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM synced_transactions WHERE import_id = ?)`, importID).Scan(&exists)
	return exists, err
}

func markSynced(db *sql.DB, importID string) error {
	_, err := db.Exec(`INSERT INTO synced_transactions (import_id) VALUES (?)`, importID)
	return err
}

// syncTransaction checks whether a transaction has already been synced, and
// if not, transforms and posts it to YNAB and records it as synced. It's
// shared by both the one-shot batch sync and the webhook handler.
func syncTransaction(db *sql.DB, txn Transaction, cfg Config) (skipped bool, err error) {
	alreadySynced, err := isSynced(db, txn.ID)
	if err != nil {
		return false, fmt.Errorf("checking sync state: %w", err)
	}
	if alreadySynced {
		return true, nil
	}

	wasDuplicate := false
	ynabTxn := transformTransaction(txn, cfg.AccountID)
	if err := postTransaction(ynabTxn, cfg.BudgetID, cfg.YnabToken); err != nil {
		if !errors.Is(err, ErrDuplicateTransaction) {
			return false, fmt.Errorf("posting to YNAB: %w", err)
		}
		// Already in YNAB from before local state tracking existed (or a
		// prior run's markSynced failed) - reconcile rather than retry forever.
		wasDuplicate = true
	}

	if err := markSynced(db, txn.ID); err != nil {
		return false, fmt.Errorf("posted to YNAB but failed to record sync state: %w", err)
	}

	return wasDuplicate, nil
}

// verifySignature checks Up's HMAC-SHA256 webhook signature against the raw
// request body, using constant-time comparison to avoid timing attacks.
func verifySignature(payload []byte, signatureHeader, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)

	got, err := hex.DecodeString(signatureHeader)
	if err != nil {
		return false
	}

	return hmac.Equal(expected, got)
}

// handleUpWebhook verifies and processes a single Up webhook event. Only
// TRANSACTION_CREATED and TRANSACTION_SETTLED events carry a transaction to
// sync; everything else (e.g. PING, TRANSACTION_DELETED) is acknowledged
// and ignored.
func handleUpWebhook(db *sql.DB, cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		if !verifySignature(body, r.Header.Get("X-Up-Authenticity-Signature"), cfg.WebhookSecret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		var event WebhookEvent
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

		txn, err := fetchTransactionByID(txnRef.ID, cfg.UpToken)
		if err != nil {
			log.Printf("failed to fetch transaction %s: %v", txnRef.ID, err)
			http.Error(w, "failed to fetch transaction", http.StatusInternalServerError)
			return
		}

		skipped, err := syncTransaction(db, txn, cfg)
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

// runOneShotSync fetches the current page of Up transactions and syncs any
// that haven't already been synced to YNAB.
func runOneShotSync(db *sql.DB, cfg Config) {
	transactions, err := fetchTransactions(cfg.UpToken)
	if err != nil {
		log.Fatalf("failed to fetch transactions: %v", err)
	}

	if len(transactions) == 0 {
		log.Fatal("no transactions returned from Up")
	}

	synced, skipped, failed := 0, 0, 0

	for _, txn := range transactions {
		wasSkipped, err := syncTransaction(db, txn, cfg)
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

// runServer starts the HTTP server that receives Up's webhooks.
func runServer(db *sql.DB, cfg Config, port string) {
	http.HandleFunc("/webhooks/up", handleUpWebhook(db, cfg))
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func main() {
	serve := flag.Bool("serve", false, "run as a webhook server instead of a one-shot sync")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, falling back to existing environment")
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = DefaultDBPath
	}

	db, err := initDB(dbPath)
	if err != nil {
		log.Fatalf("failed to open state db: %v", err)
	}
	defer db.Close()

	cfg := Config{
		UpToken:       os.Getenv("UP_API_TOKEN"),
		WebhookSecret: os.Getenv("UP_WEBHOOK_SECRET"),
		AccountID:     os.Getenv("YNAB_ACCOUNT_ID"),
		BudgetID:      os.Getenv("YNAB_BUDGET_ID"),
		YnabToken:     os.Getenv("YNAB_API_TOKEN"),
	}

	if *serve {
		port := os.Getenv("PORT")
		if port == "" {
			port = DefaultPort
		}
		runServer(db, cfg, port)
		return
	}

	runOneShotSync(db, cfg)
}
