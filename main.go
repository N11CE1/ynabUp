package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"

	"github.com/N11CE1/ynabUp/banksync"
	"github.com/N11CE1/ynabUp/pipeline"
	"github.com/N11CE1/ynabUp/store"
	"github.com/N11CE1/ynabUp/webhook"
	"github.com/N11CE1/ynabUp/ynab"
)

const (
	DefaultDBPath = "sync.db"
	DefaultPort   = "8080"
)

// runServer starts the HTTP server that receives Up's and BankSync's webhooks.
func runServer(db *sql.DB, cfg pipeline.Config, port string) {
	http.HandleFunc("/webhooks/up", webhook.Handler(db, cfg))
	http.HandleFunc("/webhooks/banksync", banksync.Handler(db, cfg))

	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// parseAccountMap parses a JSON object env var mapping a source account ID
// to the YNAB account it syncs into (used for both UP_ACCOUNT_MAP and
// BANKSYNC_ACCOUNT_MAP).
func parseAccountMap(envVar string) map[string]string {
	raw := os.Getenv(envVar)
	if raw == "" {
		return nil
	}

	var accountMap map[string]string
	if err := json.Unmarshal([]byte(raw), &accountMap); err != nil {
		log.Fatalf("failed to parse %s: %v", envVar, err)
	}

	return accountMap
}

// fetchYnabTransferPayeeIDs looks up each account's transfer payee ID, used
// to post real linked transfers between two mapped accounts.
func fetchYnabTransferPayeeIDs(budgetID, token string) map[string]string {
	accounts, err := ynab.FetchAccounts(budgetID, token)
	if err != nil {
		log.Fatalf("failed to fetch YNAB accounts: %v", err)
	}

	ids := make(map[string]string, len(accounts))
	for _, a := range accounts {
		ids[a.ID] = a.TransferPayeeID
	}

	return ids
}

func main() {
	serve := flag.Bool("serve", false, "run as a webhook server instead of a one-shot sync")
	backfill := flag.Bool("backfill", false, "backfill BankSync transactions for a date range instead of a one-shot sync")
	from := flag.String("from", "", "backfill start date (YYYY-MM-DD), defaults to the 1st of the current month")
	to := flag.String("to", "", "backfill end date (YYYY-MM-DD), defaults to today")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, falling back to existing environment")
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = DefaultDBPath
	}

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open state db: %v", err)
	}
	defer db.Close()

	budgetID := os.Getenv("YNAB_BUDGET_ID")
	ynabToken := os.Getenv("YNAB_API_TOKEN")

	cfg := pipeline.Config{
		UpToken:               os.Getenv("UP_API_TOKEN"),
		UpWebhookSecret:       os.Getenv("UP_WEBHOOK_SECRET"),
		UpAccountMap:          parseAccountMap("UP_ACCOUNT_MAP"),
		BankSyncAPIToken:      os.Getenv("BANKSYNC_API_TOKEN"),
		BankSyncWebhookSecret: os.Getenv("BANKSYNC_WEBHOOK_SECRET"),
		BankSyncAccountMap:    parseAccountMap("BANKSYNC_ACCOUNT_MAP"),
		YnabTransferPayeeIDs:  fetchYnabTransferPayeeIDs(budgetID, ynabToken),
		BudgetID:              budgetID,
		YnabToken:             ynabToken,
	}

	if *serve {
		port := os.Getenv("PORT")
		if port == "" {
			port = DefaultPort
		}
		runServer(db, cfg, port)
		return
	}

	if *backfill {
		now := time.Now()
		fromDate := *from
		if fromDate == "" {
			fromDate = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
		}
		toDate := *to
		if toDate == "" {
			toDate = now.Format("2006-01-02")
		}

		bankID := os.Getenv("BANKSYNC_BANK_ID")
		if bankID == "" {
			log.Fatal("BANKSYNC_BANK_ID must be set to backfill")
		}

		banksync.RunBackfill(db, cfg, bankID, fromDate, toDate)
		return
	}

	pipeline.RunOneShotSync(db, cfg)
}
