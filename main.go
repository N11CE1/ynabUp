package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
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
)

type TransactionResponse struct {
	Data []Transaction `json:"data"`
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

func main() {
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

	req, err := http.NewRequest("GET", APIBaseURL, nil)
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("UP_API_TOKEN"))

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("unexpected status: %s", resp.Status)
	}

	var result TransactionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Fatal(err)
	}

	if len(result.Data) == 0 {
		log.Fatal("no transactions returned from Up")
	}

	accountID := os.Getenv("YNAB_ACCOUNT_ID")
	budgetID := os.Getenv("YNAB_BUDGET_ID")
	ynabToken := os.Getenv("YNAB_API_TOKEN")

	synced, skipped, failed := 0, 0, 0

	for _, txn := range result.Data {
		alreadySynced, err := isSynced(db, txn.ID)
		if err != nil {
			log.Printf("failed to check sync state for %s: %v", txn.ID, err)
			failed++
			continue
		}
		if alreadySynced {
			skipped++
			continue
		}

		ynabTxn := transformTransaction(txn, accountID)
		if err := postTransaction(ynabTxn, budgetID, ynabToken); err != nil {
			log.Printf("failed to post transaction %s to YNAB: %v", txn.ID, err)
			failed++
			continue
		}

		if err := markSynced(db, txn.ID); err != nil {
			log.Printf("posted %s to YNAB but failed to record sync state: %v", txn.ID, err)
			failed++
			continue
		}

		fmt.Printf("Synced -> Date: %s\tAmount: %d\tPayeeName: %s\tImportID: %s\n",
			ynabTxn.Date, ynabTxn.Amount, ynabTxn.PayeeName, ynabTxn.ImportID)
		synced++
	}

	fmt.Printf("Done: %d synced, %d already synced (skipped), %d failed\n", synced, skipped, failed)
}
