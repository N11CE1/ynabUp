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

// APIBaseURL is a var, not a const, so tests can point it at a fake server.
var APIBaseURL = "https://api.ynab.com/v1"

// ErrDuplicateTransaction indicates YNAB already has a transaction with this
// import_id on the account, e.g. from a sync predating local state tracking.
var ErrDuplicateTransaction = errors.New("transaction already exists in YNAB")

// httpClient is shared across every request in this package: a bare
// &http.Client{} has no timeout, so a stalled connection to YNAB would
// otherwise hang the sync indefinitely with no recovery path - fatal for a
// service that runs unattended in a cron loop. Sharing one client also
// reuses connections instead of dialing fresh for every call.
var httpClient = &http.Client{Timeout: 30 * time.Second}

const maxRateLimitRetries = 5

// doWithRetry sends req, retrying with exponential backoff on a 429
// response. YNAB doesn't send a Retry-After header, so this only smooths
// over short bursts - if the hourly quota is genuinely exhausted, it gives
// up and returns an error. req.GetBody is used to re-read the body on a
// retry (set automatically by http.NewRequest for a *bytes.Reader body, as
// every POST/PATCH in this package uses); GET/DELETE requests have a nil
// body and don't need it.
func doWithRetry(req *http.Request) (*http.Response, error) {
	backoff := 2 * time.Second
	for attempt := 1; attempt <= maxRateLimitRetries; attempt++ {
		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("rewinding request body for retry: %w", err)
			}
			req.Body = body
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}

		resp.Body.Close()
		if attempt == maxRateLimitRetries {
			break
		}
		time.Sleep(backoff)
		backoff *= 2
	}

	return nil, fmt.Errorf("rate limited after %d attempts", maxRateLimitRetries)
}

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

type postTransactionResponse struct {
	Data struct {
		Transaction struct {
			ID string `json:"id"`
		} `json:"transaction"`
	} `json:"data"`
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

	resp, err := doWithRetry(req)
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

type Category struct {
	ID       string `json:"id"`
	Budgeted int64  `json:"budgeted"`
}

type categoryResponse struct {
	Data struct {
		Category Category `json:"category"`
	} `json:"data"`
}

type categoryUpdateRequest struct {
	Category struct {
		Budgeted int64 `json:"budgeted"`
	} `json:"category"`
}

// GetCategory retrieves categoryID's budgeted amount for the current month.
func GetCategory(budgetID, categoryID, token string) (Category, error) {
	url := fmt.Sprintf("%s/budgets/%s/months/current/categories/%s", APIBaseURL, budgetID, categoryID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return Category{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := doWithRetry(req)
	if err != nil {
		return Category{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return Category{}, fmt.Errorf("unexpected status: %s: %s", resp.Status, respBody)
	}

	var result categoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Category{}, err
	}

	return result.Data.Category, nil
}

// FundCategory increases categoryID's current-month budgeted amount by
// deltaMilliunits. Used to reflect a transfer into a savings account in the
// category YNAB otherwise never touches, since a transfer moves cash
// between accounts without assigning any category dollars. Not atomic - a
// concurrent budget edit between the read and the write here could be
// clobbered, an acceptable risk for a single-instance, low-volume service.
func FundCategory(budgetID, categoryID string, deltaMilliunits int64, token string) error {
	current, err := GetCategory(budgetID, categoryID, token)
	if err != nil {
		return fmt.Errorf("reading category before funding: %w", err)
	}

	var update categoryUpdateRequest
	update.Category.Budgeted = current.Budgeted + deltaMilliunits

	body, err := json.Marshal(update)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/budgets/%s/months/current/categories/%s", APIBaseURL, budgetID, categoryID)
	req, err := http.NewRequest("PATCH", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := doWithRetry(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status: %s: %s", resp.Status, respBody)
	}

	return nil
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

// PostTransaction sends a single transformed transaction to YNAB's API,
// retrying with exponential backoff if rate-limited, and returns YNAB's own
// ID for the created transaction (needed later to delete it, since YNAB's
// delete endpoint is keyed by its ID, not import_id). YNAB doesn't send a
// Retry-After header, so this only smooths over short bursts - if the
// hourly quota is genuinely exhausted, it gives up and returns an error;
// callers can safely retry later since posting is idempotent on import_id.
// On ErrDuplicateTransaction the returned ID is empty, since YNAB's 409
// response doesn't disclose the existing transaction's ID.
func PostTransaction(txn Transaction, budgetID, token string) (ynabTransactionID string, err error) {
	body, err := json.Marshal(transactionRequest{Transaction: txn})
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/budgets/%s/transactions", APIBaseURL, budgetID)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := doWithRetry(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return "", ErrDuplicateTransaction
	}

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status: %s: %s", resp.Status, respBody)
	}

	var result postTransactionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding created transaction: %w", err)
	}

	return result.Data.Transaction.ID, nil
}

type clearedUpdateRequest struct {
	Transaction struct {
		Cleared string `json:"cleared"`
	} `json:"transaction"`
}

// UpdateTransactionCleared sets a transaction's cleared status by its own ID
// (not import_id). Used when Up reports a transaction settling after it was
// already synced as uncleared - posting only happens once per import_id, so
// a later status change has to be applied as an update to the existing YNAB
// transaction rather than a new post.
func UpdateTransactionCleared(budgetID, transactionID, cleared, token string) error {
	var update clearedUpdateRequest
	update.Transaction.Cleared = cleared

	body, err := json.Marshal(update)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/budgets/%s/transactions/%s", APIBaseURL, budgetID, transactionID)
	req, err := http.NewRequest("PATCH", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := doWithRetry(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status: %s: %s", resp.Status, respBody)
	}

	return nil
}

// DeleteTransaction removes a transaction from YNAB by its own ID (not
// import_id - the delete endpoint doesn't support that). Used when Up
// reports a transaction as deleted (e.g. a released authorization hold
// replaced by a separate settled transaction) and we'd already synced it.
// A 404 (already gone, e.g. deleted by hand or by a prior retry) is treated
// as success rather than an error, since the end state is what we wanted.
func DeleteTransaction(budgetID, transactionID, token string) error {
	url := fmt.Sprintf("%s/budgets/%s/transactions/%s", APIBaseURL, budgetID, transactionID)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := doWithRetry(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status: %s: %s", resp.Status, respBody)
	}

	return nil
}
