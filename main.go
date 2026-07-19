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

	"github.com/N11CE1/ynabUp/banksync"
	"github.com/N11CE1/ynabUp/pipeline"
	"github.com/N11CE1/ynabUp/store"
	"github.com/N11CE1/ynabUp/webhook"
	"github.com/N11CE1/ynabUp/ynab"
)

const (
	DefaultDBPath       = "sync.db"
	DefaultPort         = "8080"
	DefaultCronInterval = time.Hour
	// bankSyncCronLookback bounds each cron pass's BankSync backfill to a
	// short rolling window, rather than rescanning the whole month every
	// time - generous enough to catch anything that arrived late.
	bankSyncCronLookback = 3 * 24 * time.Hour
)

// runServer starts the HTTP server that receives Up's and BankSync's
// webhooks, and kicks off a background reconciliation loop alongside it -
// a safety net for Up (in case a webhook was missed) and, since BankSync's
// own webhook delivery is currently broken on their end, the only way
// BankSync data gets synced at all.
func runServer(db *sql.DB, cfg pipeline.Config, port, bankID string, cronInterval time.Duration) {
	http.HandleFunc("/webhooks/up", webhook.Handler(db, cfg))
	http.HandleFunc("/webhooks/banksync", banksync.Handler(db, cfg))

	go runCronLoop(db, cfg, bankID, cronInterval)

	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// runCronLoop runs a reconciliation pass immediately, then every interval
// thereafter, for as long as the process is running.
func runCronLoop(db *sql.DB, cfg pipeline.Config, bankID string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		runReconciliation(db, cfg, bankID)
		<-ticker.C
	}
}

func runReconciliation(db *sql.DB, cfg pipeline.Config, bankID string) {
	log.Println("cron: running Up reconciliation sync")
	if err := pipeline.RunOneShotSync(db, cfg); err != nil {
		log.Printf("cron: Up reconciliation failed: %v", err)
	}

	if bankID == "" || len(cfg.BankSyncAccountMap) == 0 {
		return
	}

	log.Println("cron: running BankSync backfill")
	now := time.Now()
	from := now.Add(-bankSyncCronLookback).Format("2006-01-02")
	to := now.Format("2006-01-02")
	banksync.RunBackfill(db, cfg, bankID, from, to)
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
		SavingsAccountIDs:     parseAccountIDSet("YNAB_SAVINGS_ACCOUNT_IDS"),
		SavingsCategoryID:     os.Getenv("YNAB_SAVINGS_CATEGORY_ID"),
		BudgetID:              budgetID,
		YnabToken:             ynabToken,
	}

	bankID := os.Getenv("BANKSYNC_BANK_ID")

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

		runServer(db, cfg, port, bankID, cronInterval)
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

		if bankID == "" {
			log.Fatal("BANKSYNC_BANK_ID must be set to backfill")
		}

		banksync.RunBackfill(db, cfg, bankID, fromDate, toDate)
		return
	}

	if err := pipeline.RunOneShotSync(db, cfg); err != nil {
		log.Fatalf("one-shot sync failed: %v", err)
	}
}
