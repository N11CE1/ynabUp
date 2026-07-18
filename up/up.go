package up

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const APIBaseURL = "https://api.up.com.au/api/v1/transactions"

type TransactionResponse struct {
	Data  []Transaction `json:"data"`
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
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
		// TransactionType distinguishes a real two-sided transfer
		// ("Transfer", with a matching record on the other account) from
		// one-sided attributions like "Round Up" or "Cover", which set
		// TransferAccount for display purposes only and have no paired
		// transaction to link against.
		TransactionType string `json:"transactionType"`
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

// FetchTransactions retrieves transactions from Up, following pagination
// until exhausted. If since is non-nil, only transactions created at or
// after it are returned (via Up's filter[since]), instead of pulling full
// history every call.
func FetchTransactions(token string, since *time.Time) ([]Transaction, error) {
	reqURL := APIBaseURL
	if since != nil {
		v := url.Values{}
		v.Set("filter[since]", since.Format(time.RFC3339))
		reqURL = APIBaseURL + "?" + v.Encode()
	}

	var all []Transaction
	for reqURL != "" {
		req, err := http.NewRequest("GET", reqURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("unexpected status: %s", resp.Status)
		}

		var result TransactionResponse
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		all = append(all, result.Data...)

		if result.Links.Next == nil {
			reqURL = ""
		} else {
			reqURL = *result.Links.Next
		}
	}

	return all, nil
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
