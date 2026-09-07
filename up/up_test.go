package up

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parsing test time %q: %v", value, err)
	}
	return parsed
}

// withFakeUp points APIBaseURL at a fake Up server for the duration of the
// test, restoring the real value on cleanup.
func withFakeUp(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)

	original := APIBaseURL
	APIBaseURL = server.URL
	t.Cleanup(func() {
		server.Close()
		APIBaseURL = original
	})

	return server
}

func TestFetchTransactions_FollowsPagination(t *testing.T) {
	var server *httptest.Server
	server = withFakeUp(t, func(w http.ResponseWriter, r *http.Request) {
		var resp TransactionResponse

		if r.URL.Query().Get("page") == "2" {
			resp.Data = []Transaction{{ID: "txn-2"}}
			// No further pages.
		} else {
			resp.Data = []Transaction{{ID: "txn-1"}}
			next := server.URL + "?page=2"
			resp.Links.Next = &next
		}

		json.NewEncoder(w).Encode(resp)
	})

	txns, err := FetchTransactions("token", nil)
	if err != nil {
		t.Fatalf("FetchTransactions: %v", err)
	}

	if len(txns) != 2 {
		t.Fatalf("expected 2 transactions across both pages, got %d", len(txns))
	}
	if txns[0].ID != "txn-1" || txns[1].ID != "txn-2" {
		t.Fatalf("expected txn-1 then txn-2 in order, got %v", txns)
	}
}

func TestFetchTransactions_SinceSetsFilterParam(t *testing.T) {
	var gotFilter string
	withFakeUp(t, func(w http.ResponseWriter, r *http.Request) {
		gotFilter = r.URL.Query().Get("filter[since]")
		json.NewEncoder(w).Encode(TransactionResponse{})
	})

	since := mustParseTime(t, "2026-03-14T09:30:00Z")
	if _, err := FetchTransactions("token", &since); err != nil {
		t.Fatalf("FetchTransactions: %v", err)
	}

	if gotFilter == "" {
		t.Fatal("expected filter[since] to be set on the request")
	}
}

func TestFetchTransactions_NoSinceOmitsFilterParam(t *testing.T) {
	var gotQuery url.Values
	withFakeUp(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		json.NewEncoder(w).Encode(TransactionResponse{})
	})

	if _, err := FetchTransactions("token", nil); err != nil {
		t.Fatalf("FetchTransactions: %v", err)
	}

	if gotQuery.Has("filter[since]") {
		t.Fatal("expected no filter[since] param when since is nil")
	}
}

func TestFetchTransactions_NonOKStatusReturnsError(t *testing.T) {
	withFakeUp(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := FetchTransactions("token", nil); err == nil {
		t.Fatal("expected an error on a non-200 response")
	}
}

func TestFetchTransactionByID_ReturnsSingleTransaction(t *testing.T) {
	withFakeUp(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(SingleTransactionResponse{Data: Transaction{ID: "txn-1"}})
	})

	txn, err := FetchTransactionByID("txn-1", "token")
	if err != nil {
		t.Fatalf("FetchTransactionByID: %v", err)
	}
	if txn.ID != "txn-1" {
		t.Fatalf("expected txn-1, got %q", txn.ID)
	}
}
