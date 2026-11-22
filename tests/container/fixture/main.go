//go:build container_smoke

package main

import (
	"log"
	"net/http"
	"time"
)

func main() {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s", r.Method, r.Host, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		switch {
		case r.Host == "api.ynab.com" && r.URL.Path == "/v1/budgets/test-budget/accounts":
			if r.Header.Get("Authorization") != "Bearer fake-ynab-token" {
				http.Error(w, "unexpected token", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"data":{"accounts":[{"id":"test-account","transfer_payee_id":"test-payee"}]}}`))
		case r.Host == "api.up.com.au" && r.URL.Path == "/api/v1/transactions":
			if r.Header.Get("Authorization") != "Bearer fake-up-token" {
				http.Error(w, "unexpected token", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"data":[],"links":{"next":null}}`))
		default:
			http.NotFound(w, r)
		}
	})
	server := &http.Server{Addr: ":443", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServeTLS("/tmp/api.crt", "/tmp/api.key"))
}
