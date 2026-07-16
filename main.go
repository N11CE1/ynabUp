package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"

	"github.com/N11CE1/ynabUp.git/banksync"
	"github.com/N11CE1/ynabUp.git/pipeline"
	"github.com/N11CE1/ynabUp.git/store"
	"github.com/N11CE1/ynabUp.git/webhook"
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

// parseBankSyncAccountMap parses the BANKSYNC_ACCOUNT_MAP env var, a JSON
// object mapping BankSync account IDs to the YNAB account they sync into.
func parseBankSyncAccountMap(raw string) map[string]string {
	if raw == "" {
		return nil
	}

	var accountMap map[string]string
	if err := json.Unmarshal([]byte(raw), &accountMap); err != nil {
		log.Fatalf("failed to parse BANKSYNC_ACCOUNT_MAP: %v", err)
	}

	return accountMap
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

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open state db: %v", err)
	}
	defer db.Close()

	cfg := pipeline.Config{
		UpToken:               os.Getenv("UP_API_TOKEN"),
		UpWebhookSecret:       os.Getenv("UP_WEBHOOK_SECRET"),
		UpAccountID:           os.Getenv("YNAB_ACCOUNT_ID"),
		BankSyncWebhookSecret: os.Getenv("BANKSYNC_WEBHOOK_SECRET"),
		BankSyncAccountMap:    parseBankSyncAccountMap(os.Getenv("BANKSYNC_ACCOUNT_MAP")),
		BudgetID:              os.Getenv("YNAB_BUDGET_ID"),
		YnabToken:             os.Getenv("YNAB_API_TOKEN"),
	}

	if *serve {
		port := os.Getenv("PORT")
		if port == "" {
			port = DefaultPort
		}
		runServer(db, cfg, port)
		return
	}

	pipeline.RunOneShotSync(db, cfg)
}
