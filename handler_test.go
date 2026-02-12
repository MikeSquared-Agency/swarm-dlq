package dlq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// mockDLQStore is a minimal in-memory store for handler tests.
type mockDLQStore struct {
	entries map[string]*Entry
}

func newMockDLQStore() *mockDLQStore {
	return &mockDLQStore{entries: make(map[string]*Entry)}
}

func (m *mockDLQStore) seed(entries ...Entry) {
	for i := range entries {
		e := entries[i]
		m.entries[e.DLQID] = &e
	}
}

// We test handlers via a chi router that uses the real Handler but a fake store.
// Since Handler takes *Store (pgx-backed), we test at the HTTP level by creating
// a test router that mimics the handler behavior with our mock.
// For true unit tests of handlers, we'd need to interface the Store — but for now
// we test the routing and JSON serialization via integration-style httptest.

func TestHandlerRoutes_Mount(t *testing.T) {
	// Verify that Routes() returns a valid router that can be mounted.
	// We can't fully test without a DB, but we can check the routes don't panic.
	r := chi.NewRouter()

	// Mount with nil store/nc — just testing route registration doesn't panic.
	h := &Handler{}
	r.Mount("/api/v1/dlq", h.Routes())

	// Check that routes are registered by hitting them (they'll fail but not 404).
	paths := []string{
		"/api/v1/dlq/",
		"/api/v1/dlq/stats",
		"/api/v1/dlq/some-id",
	}

	for _, path := range paths {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()

		// This will panic/500 due to nil store, but should NOT 404.
		func() {
			defer func() { recover() }()
			r.ServeHTTP(w, req)
		}()

		// If we get here without a 404, the route is registered.
		// (We expect 500 or panic, not 404 or 405)
		if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
			t.Errorf("route %s returned %d, expected it to be registered", path, w.Code)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]string{"key": "value"})

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}

	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["key"] != "value" {
		t.Errorf("expected value, got %s", body["key"])
	}
}

func TestEntryJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	entry := Entry{
		DLQID:           "abc-123",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task_id":"t1"}`),
		Reason:          ReasonNoCapableAgent,
		ReasonDetail:    "No agent found",
		FailedAt:        now,
		RetryCount:      3,
		MaxRetries:      3,
		RetryHistory: []RetryAttempt{
			{Attempt: 1, AttemptedAt: now, Agent: "scout", FailureReason: "unavailable"},
		},
		Source:      SourceDispatch,
		Recoverable: true,
		Recovered:   false,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Entry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.DLQID != entry.DLQID {
		t.Errorf("dlq_id mismatch")
	}
	if decoded.Reason != entry.Reason {
		t.Errorf("reason mismatch")
	}
	if len(decoded.RetryHistory) != 1 {
		t.Errorf("expected 1 retry, got %d", len(decoded.RetryHistory))
	}
	if decoded.RecoveredAt != nil {
		t.Error("expected nil recovered_at")
	}

	_ = context.Background() // suppress unused import
}

func TestStatsJSON(t *testing.T) {
	stats := Stats{
		Total:       10,
		Unrecovered: 5,
		Recoverable: 3,
		ByReason:    map[string]int{ReasonNoCapableAgent: 3, ReasonBootFailure: 2},
		BySource:    map[string]int{SourceDispatch: 3, SourceWarren: 2},
	}

	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal stats: %v", err)
	}

	var decoded Stats
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}

	if decoded.Total != 10 {
		t.Errorf("expected total 10, got %d", decoded.Total)
	}
	if decoded.ByReason[ReasonNoCapableAgent] != 3 {
		t.Errorf("expected 3 no_capable_agent, got %d", decoded.ByReason[ReasonNoCapableAgent])
	}
}
