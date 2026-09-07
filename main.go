package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/N11CE1/ynabUp/pipeline"
	"github.com/N11CE1/ynabUp/store"
	"github.com/N11CE1/ynabUp/webhook"
	"github.com/N11CE1/ynabUp/ynab"
)

const (
	DefaultDBPath       = "sync.db"
	DefaultPort         = "8080"
	DefaultCronInterval = time.Hour
)

// runServer starts the HTTP server that receives Up's webhook, and kicks off
// a background reconciliation loop alongside it as a safety net in case a
// webhook was missed.
func runServer(db *sql.DB, cfg pipeline.Config, port string, cronInterval time.Duration) {
	http.HandleFunc("/webhooks/up", webhook.Handler(db, cfg))
	http.HandleFunc("/healthz", healthzHandler(db))

	go runCronLoop(db, cfg, cronInterval)

	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// healthzHandler reports whether the state DB is reachable and when the
// last reconciliation pass completed successfully, so an external monitor
// (or a Docker HEALTHCHECK) can tell the difference between "up" and
// "up but silently failing every sync".
func healthzHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "database unreachable"})
			return
		}

		resp := map[string]any{"status": "ok"}
		if lastSynced, has, err := pipeline.LastSyncedAt(db); err == nil && has {
			resp["last_synced_at"] = lastSynced.Format(time.RFC3339)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// runCronLoop runs a reconciliation pass immediately, then every interval
// thereafter, for as long as the process is running.
func runCronLoop(db *sql.DB, cfg pipeline.Config, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		log.Println("cron: running Up reconciliation sync")
		if err := pipeline.RunOneShotSync(db, cfg); err != nil {
			log.Printf("cron: Up reconciliation failed: %v", err)
		}
		<-ticker.C
	}
}

// parseAccountMap parses a JSON object env var (UP_ACCOUNT_MAP) mapping a
// source account ID to the YNAB account it syncs into.
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

// parseAccountIDSet parses a comma-separated list of YNAB account IDs,
// used to identify which accounts should trigger a savings category
// funding bump when a linked transfer lands in them.
func parseAccountIDSet(envVar string) map[string]bool {
	raw := os.Getenv(envVar)
	if raw == "" {
		return nil
	}

	ids := make(map[string]bool)
	for id := range strings.SplitSeq(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = true
		}
	}

	return ids
}

// fetchYnabTransferPayeeIDs looks up each account's transfer payee ID, used
// to post real linked transfers between two mapped accounts.
func fetchYnabTransferPayeeIDs(budgetID, token string) (map[string]string, error) {
	accounts, err := ynab.FetchAccounts(budgetID, token)
	if err != nil {
		return nil, err
	}

	ids := make(map[string]string, len(accounts))
	for _, a := range accounts {
		ids[a.ID] = a.TransferPayeeID
	}

	return ids, nil
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

	budgetID := os.Getenv("YNAB_BUDGET_ID")
	ynabToken := os.Getenv("YNAB_API_TOKEN")

	cfg := pipeline.Config{
		UpToken:           os.Getenv("UP_API_TOKEN"),
		UpWebhookSecret:   os.Getenv("UP_WEBHOOK_SECRET"),
		UpAccountMap:      parseAccountMap("UP_ACCOUNT_MAP"),
		SavingsAccountIDs: parseAccountIDSet("YNAB_SAVINGS_ACCOUNT_IDS"),
		SavingsCategoryID: os.Getenv("YNAB_SAVINGS_CATEGORY_ID"),
		BudgetID:          budgetID,
		YnabToken:         ynabToken,
	}

	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	transferPayeeIDs, err := fetchYnabTransferPayeeIDs(budgetID, ynabToken)
	if err != nil {
		log.Fatalf("failed to fetch YNAB accounts: %v", err)
	}
	cfg.YnabTransferPayeeIDs = transferPayeeIDs

	if *serve {
		port := os.Getenv("PORT")
		if port == "" {
			port = DefaultPort
		}

		cronInterval := DefaultCronInterval
		if raw := os.Getenv("CRON_INTERVAL"); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil {
				log.Fatalf("failed to parse CRON_INTERVAL %q: %v", raw, err)
			}
			cronInterval = d
		}

		runServer(db, cfg, port, cronInterval)
		return
	}

	if err := pipeline.RunOneShotSync(db, cfg); err != nil {
		log.Fatalf("one-shot sync failed: %v", err)
	}
}
