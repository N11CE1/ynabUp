package ynab

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/N11CE1/ynabUp/up"
)

const maxImportIDLength = 36

// SafeImportID returns id unchanged if it fits YNAB's 36-character
// import_id limit; otherwise it returns a deterministic, truncated
// SHA-256 hash of id, so a given source transaction always maps to the
// same import_id across runs.
func SafeImportID(id string) string {
	if len(id) <= maxImportIDLength {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:maxImportIDLength]
}

const APIBaseURL = "https://api.ynab.com/v1"

// ErrDuplicateTransaction indicates YNAB already has a transaction with this
// import_id on the account, e.g. from a sync predating local state tracking.
var ErrDuplicateTransaction = errors.New("transaction already exists in YNAB")

type Transaction struct {
	AccountID string `json:"account_id"`
	Date      string `json:"date"`
	Amount    int64  `json:"amount"`
	// PayeeID and PayeeName are mutually exclusive: set PayeeID (to a
	// destination account's TransferPayeeID) to create a real linked
	// transfer, or PayeeName for a normal transaction.
	PayeeID   string `json:"payee_id,omitempty"`
	PayeeName string `json:"payee_name,omitempty"`
	Cleared   string `json:"cleared"`
	Approved  bool   `json:"approved"`
	ImportID  string `json:"import_id"`
}

type transactionRequest struct {
	Transaction Transaction `json:"transaction"`
}

type Account struct {
	ID string `json:"id"`
	// TransferPayeeID is the special payee that, when set as a
	// transaction's PayeeID, makes YNAB create a real linked transfer
	// into this account instead of a normal transaction.
	TransferPayeeID string `json:"transfer_payee_id"`
}

type accountsResponse struct {
	Data struct {
		Accounts []Account `json:"accounts"`
	} `json:"data"`
}

// FetchAccounts retrieves every account in the budget, used to look up each
// account's TransferPayeeID for posting real transfers.
func FetchAccounts(budgetID, token string) ([]Account, error) {
	url := fmt.Sprintf("%s/budgets/%s/accounts", APIBaseURL, budgetID)
	req, err := http.NewRequest("GET", url, nil)
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
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status: %s: %s", resp.Status, respBody)
	}

	var result accountsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Data.Accounts, nil
}

// Transform converts an Up transaction into YNAB's expected shape:
// cents -> milliunits, and Up's settledAt presence -> YNAB's cleared status.
func Transform(txn up.Transaction, accountID string) Transaction {
	cleared := "uncleared"
	if txn.Attributes.SettledAt != nil {
		cleared = "cleared"
	}

	return Transaction{
		AccountID: accountID,
		Date:      txn.Attributes.CreatedAt.Format("2006-01-02"),
		Amount:    txn.Attributes.Amount.ValueInBaseUnits * 10,
		PayeeName: txn.Attributes.Description,
		Cleared:   cleared,
		Approved:  false,
		ImportID:  SafeImportID(txn.ID),
	}
}

const maxRateLimitRetries = 5

// PostTransaction sends a single transformed transaction to YNAB's API,
// retrying with exponential backoff if rate-limited. YNAB doesn't send a
// Retry-After header, so this only smooths over short bursts - if the
// hourly quota is genuinely exhausted, it gives up and returns an error;
// callers can safely retry later since posting is idempotent on import_id.
func PostTransaction(txn Transaction, budgetID, token string) error {
	body, err := json.Marshal(transactionRequest{Transaction: txn})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/budgets/%s/transactions", APIBaseURL, budgetID)

	backoff := 2 * time.Second
	for attempt := 1; attempt <= maxRateLimitRetries; attempt++ {
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

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			if attempt == maxRateLimitRetries {
				return fmt.Errorf("rate limited after %d attempts", maxRateLimitRetries)
			}
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			return ErrDuplicateTransaction
		}

		if resp.StatusCode != http.StatusCreated {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("unexpected status: %s: %s", resp.Status, respBody)
		}

		resp.Body.Close()
		return nil
	}

	return fmt.Errorf("rate limited after %d attempts", maxRateLimitRetries)
}
