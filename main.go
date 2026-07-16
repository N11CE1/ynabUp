package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

const (
	APIBaseURL = "https://api.up.com.au/api/v1/transactions"
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

	for _, txn := range result.Data {
		settledAt := "pending"
		if txn.Attributes.SettledAt != nil {
			settledAt = txn.Attributes.SettledAt.String()
		}
		fmt.Printf("ID: %s\tDescription: %s\tAmount: %d\tCreatedAt: %s\tSettledAt: %s\n",
			txn.ID,
			txn.Attributes.Description,
			txn.Attributes.Amount.ValueInBaseUnits,
			txn.Attributes.CreatedAt,
			settledAt,
		)
	}
}
