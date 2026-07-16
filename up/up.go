package up

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const APIBaseURL = "https://api.up.com.au/api/v1/transactions"

type TransactionResponse struct {
	Data []Transaction `json:"data"`
}

type SingleTransactionResponse struct {
	Data Transaction `json:"data"`
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
	Relationships struct {
		Account struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		} `json:"account"`
		// TransferAccount is only present when this transaction is an
		// internal transfer between two of the user's own Up accounts.
		TransferAccount struct {
			Data *struct {
				ID string `json:"id"`
			} `json:"data"`
		} `json:"transferAccount"`
	} `json:"relationships"`
}

// FetchTransactions retrieves the most recent page of transactions from Up.
func FetchTransactions(token string) ([]Transaction, error) {
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

// FetchTransactionByID retrieves a single transaction, used by the webhook
// handler since Up's webhook payload only contains a transaction ID.
func FetchTransactionByID(id, token string) (Transaction, error) {
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
