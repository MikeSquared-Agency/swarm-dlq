package dlq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func newTestRouter(store DataStore, nc NATSPublisher) http.Handler {
	r := chi.NewRouter()
	h := NewHandler(store, nc)
	r.Mount("/dlq", h.Routes())
	return r
}

func TestHandler_List_Empty(t *testing.T) {
	store := newMockStore()
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("GET", "/dlq/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var entries []Entry
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 0 {
		t.Errorf("expected empty list, got %d entries", len(entries))
	}
}

func TestHandler_List_WithEntries(t *testing.T) {
	store := newMockStore()
	store.seed(
		Entry{DLQID: "e1", Reason: ReasonNoCapableAgent, Source: SourceDispatch},
		Entry{DLQID: "e2", Reason: ReasonBootFailure, Source: SourceWarren},
		Entry{DLQID: "e3", Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recovered: true},
	)
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("GET", "/dlq/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var entries []Entry
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

func TestHandler_List_FilterByRecovered(t *testing.T) {
	store := newMockStore()
	store.seed(
		Entry{DLQID: "e1", Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recovered: false},
		Entry{DLQID: "e2", Reason: ReasonBootFailure, Source: SourceWarren, Recovered: true},
	)
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("GET", "/dlq/?recovered=false", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var entries []Entry
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
	if len(entries) > 0 && entries[0].DLQID != "e1" {
		t.Errorf("expected e1, got %s", entries[0].DLQID)
	}
}

func TestHandler_List_FilterByReason(t *testing.T) {
	store := newMockStore()
	store.seed(
		Entry{DLQID: "e1", Reason: ReasonNoCapableAgent, Source: SourceDispatch},
		Entry{DLQID: "e2", Reason: ReasonBootFailure, Source: SourceWarren},
	)
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("GET", "/dlq/?reason=boot_failure", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var entries []Entry
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestHandler_List_FilterBySource(t *testing.T) {
	store := newMockStore()
	store.seed(
		Entry{DLQID: "e1", Reason: ReasonNoCapableAgent, Source: SourceDispatch},
		Entry{DLQID: "e2", Reason: ReasonBootFailure, Source: SourceWarren},
	)
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("GET", "/dlq/?source=warren", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var entries []Entry
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestHandler_List_StoreError(t *testing.T) {
	store := newMockStore()
	store.listErr = fmt.Errorf("db down")
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("GET", "/dlq/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestHandler_Get_Found(t *testing.T) {
	store := newMockStore()
	store.seed(Entry{
		DLQID:           "abc-123",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task":"t1"}`),
		Reason:          ReasonNoCapableAgent,
		Source:          SourceDispatch,
	})
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("GET", "/dlq/abc-123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var entry Entry
	json.NewDecoder(w.Body).Decode(&entry)
	if entry.DLQID != "abc-123" {
		t.Errorf("expected abc-123, got %s", entry.DLQID)
	}
	if entry.Reason != ReasonNoCapableAgent {
		t.Errorf("expected %s, got %s", ReasonNoCapableAgent, entry.Reason)
	}
}

func TestHandler_Get_NotFound(t *testing.T) {
	store := newMockStore()
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("GET", "/dlq/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_Retry_Success(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	store.seed(Entry{
		DLQID:           "retry-1",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task_id":"t1"}`),
		Reason:          ReasonNoCapableAgent,
		Source:          SourceDispatch,
		Recoverable:     true,
	})
	r := newTestRouter(store, nc)

	req := httptest.NewRequest("POST", "/dlq/retry-1/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	// Verify NATS publish happened.
	msgs := nc.published()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}
	if msgs[0].Subject != "swarm.task.request" {
		t.Errorf("expected subject swarm.task.request, got %s", msgs[0].Subject)
	}

	// Verify marked recovered.
	entry, _ := store.Get(context.TODO(), "retry-1")
	if !entry.Recovered {
		t.Error("expected entry to be marked recovered")
	}
	if entry.RecoveredBy != "api-retry" {
		t.Errorf("expected recovered_by api-retry, got %s", entry.RecoveredBy)
	}
}

func TestHandler_Retry_NotFound(t *testing.T) {
	store := newMockStore()
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("POST", "/dlq/nonexistent/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_Retry_AlreadyRecovered(t *testing.T) {
	store := newMockStore()
	now := time.Now().UTC()
	store.seed(Entry{
		DLQID:       "retry-2",
		Reason:      ReasonNoCapableAgent,
		Source:      SourceDispatch,
		Recovered:   true,
		RecoveredAt: &now,
		RecoveredBy: "api-retry",
	})
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("POST", "/dlq/retry-2/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}

func TestHandler_Retry_PublishFails(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	nc.err = fmt.Errorf("nats down")
	store.seed(Entry{
		DLQID:           "retry-3",
		OriginalSubject: "swarm.task.request",
		Reason:          ReasonNoCapableAgent,
		Source:          SourceDispatch,
	})
	r := newTestRouter(store, nc)

	req := httptest.NewRequest("POST", "/dlq/retry-3/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestHandler_Discard_Success(t *testing.T) {
	store := newMockStore()
	store.seed(Entry{
		DLQID:  "discard-1",
		Reason: ReasonNoCapableAgent,
		Source: SourceDispatch,
	})
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("POST", "/dlq/discard-1/discard", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	entry, _ := store.Get(context.TODO(), "discard-1")
	if !entry.Recovered {
		t.Error("expected entry to be marked recovered after discard")
	}
	if entry.RecoveredBy != "manual-discard" {
		t.Errorf("expected recovered_by manual-discard, got %s", entry.RecoveredBy)
	}
}

func TestHandler_Discard_NotFound(t *testing.T) {
	store := newMockStore()
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("POST", "/dlq/nonexistent/discard", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_RetryAll_Success(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	store.seed(
		Entry{DLQID: "ra-1", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{}`), Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recoverable: true},
		Entry{DLQID: "ra-2", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{}`), Reason: ReasonAllAgentsUnavailable, Source: SourceDispatch, Recoverable: true},
		Entry{DLQID: "ra-3", OriginalSubject: "swarm.task.request", Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recoverable: false},       // not recoverable
		Entry{DLQID: "ra-4", OriginalSubject: "swarm.task.request", Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recoverable: true, Recovered: true}, // already recovered
	)
	r := newTestRouter(store, nc)

	req := httptest.NewRequest("POST", "/dlq/retry-all", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)

	retried := int(body["retried"].(float64))
	total := int(body["total"].(float64))
	if retried != 2 {
		t.Errorf("expected 2 retried, got %d", retried)
	}
	if total != 2 {
		t.Errorf("expected 2 total recoverable, got %d", total)
	}

	msgs := nc.published()
	if len(msgs) != 2 {
		t.Errorf("expected 2 published messages, got %d", len(msgs))
	}
}

func TestHandler_RetryAll_PartialFailure(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	store.seed(
		Entry{DLQID: "pf-1", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{}`), Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recoverable: true},
		Entry{DLQID: "pf-2", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{}`), Reason: ReasonBootFailure, Source: SourceWarren, Recoverable: true},
	)

	// Have NATS fail entirely to test the error path.
	nc.err = fmt.Errorf("nats timeout")
	r := newTestRouter(store, nc)

	req := httptest.NewRequest("POST", "/dlq/retry-all", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)

	failed := int(body["failed"].(float64))
	if failed != 2 {
		t.Errorf("expected 2 failed, got %d", failed)
	}
}

func TestHandler_RetryAll_Empty(t *testing.T) {
	store := newMockStore()
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("POST", "/dlq/retry-all", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)

	total := int(body["total"].(float64))
	if total != 0 {
		t.Errorf("expected 0 total, got %d", total)
	}
}

func TestHandler_Stats(t *testing.T) {
	store := newMockStore()
	store.seed(
		Entry{DLQID: "s1", Reason: ReasonNoCapableAgent, Source: SourceDispatch},
		Entry{DLQID: "s2", Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recoverable: true},
		Entry{DLQID: "s3", Reason: ReasonBootFailure, Source: SourceWarren},
		Entry{DLQID: "s4", Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recovered: true},
	)
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("GET", "/dlq/stats", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var stats Stats
	json.NewDecoder(w.Body).Decode(&stats)

	if stats.Total != 4 {
		t.Errorf("expected total 4, got %d", stats.Total)
	}
	if stats.Unrecovered != 3 {
		t.Errorf("expected unrecovered 3, got %d", stats.Unrecovered)
	}
	if stats.Recoverable != 1 {
		t.Errorf("expected recoverable 1, got %d", stats.Recoverable)
	}
	if stats.ByReason[ReasonNoCapableAgent] != 2 {
		t.Errorf("expected 2 no_capable_agent, got %d", stats.ByReason[ReasonNoCapableAgent])
	}
	if stats.BySource[SourceWarren] != 1 {
		t.Errorf("expected 1 warren, got %d", stats.BySource[SourceWarren])
	}
}

func TestHandler_Stats_Error(t *testing.T) {
	store := newMockStore()
	store.statsErr = fmt.Errorf("db down")
	r := newTestRouter(store, newMockNATS())

	req := httptest.NewRequest("GET", "/dlq/stats", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
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
