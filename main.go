package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

const (
	APIBaseURL     = "https://api.up.com.au/api/v1/transactions"
	YNABAPIBaseURL = "https://api.ynab.com/v1"
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

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, falling back to existing environment")
	}

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

	ynabTxn := transformTransaction(result.Data[0], accountID)
	fmt.Printf("Sending to YNAB -> AccountID: %s\tDate: %s\tAmount: %d\tPayeeName: %s\tCleared: %s\tImportID: %s\n",
		ynabTxn.AccountID,
		ynabTxn.Date,
		ynabTxn.Amount,
		ynabTxn.PayeeName,
		ynabTxn.Cleared,
		ynabTxn.ImportID,
	)

	if err := postTransaction(ynabTxn, budgetID, ynabToken); err != nil {
		log.Fatalf("failed to post transaction to YNAB: %v", err)
	}

	fmt.Println("Transaction successfully created in YNAB")
}
