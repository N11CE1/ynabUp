package main

import (
	"database/sql"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"

	"github.com/N11CE1/ynabUp.git/pipeline"
	"github.com/N11CE1/ynabUp.git/store"
	"github.com/N11CE1/ynabUp.git/webhook"
)

const (
	DefaultDBPath = "sync.db"
	DefaultPort   = "8080"
)

// runServer starts the HTTP server that receives Up's webhooks.
func runServer(db *sql.DB, cfg pipeline.Config, port string) {
	http.HandleFunc("/webhooks/up", webhook.Handler(db, cfg))
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

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open state db: %v", err)
	}
	defer db.Close()

	cfg := pipeline.Config{
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

	pipeline.RunOneShotSync(db, cfg)
}
