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

// TestE2E_FullLifecycle tests the complete DLQ flow:
// 1. Processor ingests a DLQ event → writes to store
// 2. API lists/gets the entry
// 3. API retries the entry → publishes to NATS and marks recovered
// 4. Stats reflect the state changes
func TestE2E_FullLifecycle(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	ctx := context.Background()

	// --- Step 1: Processor ingests a DLQ event ---
	proc := NewProcessor(store)
	dlqEvent := Entry{
		DLQID:           "e2e-lifecycle-1",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task_id":"task-42","title":"Research competitors"}`),
		Reason:          ReasonNoCapableAgent,
		ReasonDetail:    "No agent with capabilities [research, competitor-analysis]",
		FailedAt:        time.Now().UTC(),
		RetryCount:      3,
		MaxRetries:      3,
		RetryHistory: []RetryAttempt{
			{Attempt: 1, AttemptedAt: time.Now().UTC(), Agent: "scout", FailureReason: "agent_unavailable"},
			{Attempt: 2, AttemptedAt: time.Now().UTC(), Agent: "kai", FailureReason: "boot_failure"},
			{Attempt: 3, AttemptedAt: time.Now().UTC(), FailureReason: "no_capable_agent"},
		},
		Source:      SourceDispatch,
		Recoverable: true,
	}

	eventData, _ := json.Marshal(dlqEvent)
	proc.Process(ctx, "dlq.task.unassignable", eventData)

	// Verify it was stored.
	stored, err := store.Get(ctx, "e2e-lifecycle-1")
	if err != nil {
		t.Fatalf("step 1: entry not found after Process: %v", err)
	}
	if stored.Reason != ReasonNoCapableAgent {
		t.Errorf("step 1: expected reason %s, got %s", ReasonNoCapableAgent, stored.Reason)
	}
	if len(stored.RetryHistory) != 3 {
		t.Errorf("step 1: expected 3 retry attempts, got %d", len(stored.RetryHistory))
	}

	// --- Step 2: API lists and gets the entry ---
	handler := NewHandler(store, nc)
	router := chi.NewRouter()
	router.Mount("/dlq", handler.Routes())

	// List all entries.
	req := httptest.NewRequest("GET", "/dlq/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("step 2: list returned %d", w.Code)
	}
	var entries []Entry
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 1 {
		t.Fatalf("step 2: expected 1 entry in list, got %d", len(entries))
	}

	// Get single entry.
	req = httptest.NewRequest("GET", "/dlq/e2e-lifecycle-1", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("step 2: get returned %d", w.Code)
	}
	var fetched Entry
	json.NewDecoder(w.Body).Decode(&fetched)
	if fetched.DLQID != "e2e-lifecycle-1" {
		t.Errorf("step 2: expected e2e-lifecycle-1, got %s", fetched.DLQID)
	}

	// --- Step 3: Check stats before recovery ---
	req = httptest.NewRequest("GET", "/dlq/stats", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var statsBefore Stats
	json.NewDecoder(w.Body).Decode(&statsBefore)
	if statsBefore.Total != 1 {
		t.Errorf("step 3: expected total 1, got %d", statsBefore.Total)
	}
	if statsBefore.Unrecovered != 1 {
		t.Errorf("step 3: expected unrecovered 1, got %d", statsBefore.Unrecovered)
	}
	if statsBefore.Recoverable != 1 {
		t.Errorf("step 3: expected recoverable 1, got %d", statsBefore.Recoverable)
	}

	// --- Step 4: Retry the entry via API ---
	req = httptest.NewRequest("POST", "/dlq/e2e-lifecycle-1/retry", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("step 4: retry returned %d; body: %s", w.Code, w.Body.String())
	}

	// Verify NATS message was published.
	msgs := nc.published()
	if len(msgs) != 1 {
		t.Fatalf("step 4: expected 1 NATS message, got %d", len(msgs))
	}
	if msgs[0].Subject != "swarm.task.request" {
		t.Errorf("step 4: expected subject swarm.task.request, got %s", msgs[0].Subject)
	}

	// Verify the payload matches original.
	var republished map[string]any
	json.Unmarshal(msgs[0].Data, &republished)
	if republished["task_id"] != "task-42" {
		t.Errorf("step 4: republished payload missing task_id")
	}

	// --- Step 5: Verify entry is now recovered ---
	recovered, _ := store.Get(ctx, "e2e-lifecycle-1")
	if !recovered.Recovered {
		t.Error("step 5: entry should be recovered")
	}
	if recovered.RecoveredBy != "api-retry" {
		t.Errorf("step 5: expected recovered_by api-retry, got %s", recovered.RecoveredBy)
	}

	// --- Step 6: Retry again should fail with 409 ---
	req = httptest.NewRequest("POST", "/dlq/e2e-lifecycle-1/retry", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("step 6: expected 409 on double retry, got %d", w.Code)
	}

	// --- Step 7: Stats after recovery ---
	req = httptest.NewRequest("GET", "/dlq/stats", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var statsAfter Stats
	json.NewDecoder(w.Body).Decode(&statsAfter)
	if statsAfter.Total != 1 {
		t.Errorf("step 7: expected total 1, got %d", statsAfter.Total)
	}
	if statsAfter.Unrecovered != 0 {
		t.Errorf("step 7: expected unrecovered 0, got %d", statsAfter.Unrecovered)
	}
}

// TestE2E_DiscardFlow tests the discard workflow:
// 1. Ingest an entry
// 2. Discard it
// 3. Verify it's no longer in recoverable list
func TestE2E_DiscardFlow(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	ctx := context.Background()

	proc := NewProcessor(store)
	entry := Entry{
		DLQID:           "e2e-discard-1",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task_id":"task-99"}`),
		Reason:          ReasonPolicyDenied,
		FailedAt:        time.Now().UTC(),
		Source:          SourceDispatch,
		Recoverable:     true,
	}

	data, _ := json.Marshal(entry)
	proc.Process(ctx, "dlq.task.policy_denied", data)

	// Verify it's recoverable.
	recoverable, _ := store.ListRecoverable(ctx)
	if len(recoverable) != 1 {
		t.Fatalf("expected 1 recoverable entry, got %d", len(recoverable))
	}

	// Discard via API.
	handler := NewHandler(store, nc)
	router := chi.NewRouter()
	router.Mount("/dlq", handler.Routes())

	req := httptest.NewRequest("POST", "/dlq/e2e-discard-1/discard", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("discard returned %d", w.Code)
	}

	// Verify it's no longer recoverable.
	recoverable, _ = store.ListRecoverable(ctx)
	if len(recoverable) != 0 {
		t.Errorf("expected 0 recoverable entries after discard, got %d", len(recoverable))
	}

	// Verify it's marked as manually discarded.
	discarded, _ := store.Get(ctx, "e2e-discard-1")
	if discarded.RecoveredBy != "manual-discard" {
		t.Errorf("expected recovered_by manual-discard, got %s", discarded.RecoveredBy)
	}

	// No NATS messages should have been sent (discard doesn't republish).
	if len(nc.published()) != 0 {
		t.Errorf("expected no NATS messages for discard, got %d", len(nc.published()))
	}
}

// TestE2E_ScannerRecovery tests the automated scanner flow:
// 1. Ingest multiple entries (some recoverable, some not)
// 2. Run scanner
// 3. Verify only recoverable entries were retried
func TestE2E_ScannerRecovery(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	ctx := context.Background()

	proc := NewProcessor(store)

	// Recoverable entry.
	e1 := Entry{
		DLQID:           "e2e-scan-1",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task_id":"t1"}`),
		Reason:          ReasonAllAgentsUnavailable,
		FailedAt:        time.Now().UTC(),
		Source:          SourceDispatch,
		Recoverable:     true,
	}
	d1, _ := json.Marshal(e1)
	proc.Process(ctx, "dlq.task.no_available_agent", d1)

	// Non-recoverable entry.
	e2 := Entry{
		DLQID:           "e2e-scan-2",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task_id":"t2"}`),
		Reason:          ReasonPolicyDenied,
		FailedAt:        time.Now().UTC(),
		Source:          SourceDispatch,
		Recoverable:     false,
	}
	d2, _ := json.Marshal(e2)
	proc.Process(ctx, "dlq.task.policy_denied", d2)

	// Already recovered entry.
	e3 := Entry{
		DLQID:           "e2e-scan-3",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task_id":"t3"}`),
		Reason:          ReasonNoCapableAgent,
		FailedAt:        time.Now().UTC(),
		Source:          SourceDispatch,
		Recoverable:     true,
		Recovered:       true,
		RecoveredBy:     "api-retry",
	}
	d3, _ := json.Marshal(e3)
	proc.Process(ctx, "dlq.task.unassignable", d3)

	// Run scanner.
	scanner := NewScanner(store, nc, time.Minute)
	scanner.scan(ctx)

	// Only e2e-scan-1 should have been retried.
	msgs := nc.published()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 NATS message from scanner, got %d", len(msgs))
	}
	if msgs[0].Subject != "swarm.task.request" {
		t.Errorf("expected subject swarm.task.request, got %s", msgs[0].Subject)
	}

	// Verify e2e-scan-1 is now recovered.
	s1, _ := store.Get(ctx, "e2e-scan-1")
	if !s1.Recovered {
		t.Error("e2e-scan-1 should be recovered after scanner")
	}
	if s1.RecoveredBy != "auto-scanner" {
		t.Errorf("expected recovered_by auto-scanner, got %s", s1.RecoveredBy)
	}

	// e2e-scan-2 should NOT be recovered.
	s2, _ := store.Get(ctx, "e2e-scan-2")
	if s2.Recovered {
		t.Error("e2e-scan-2 should NOT be recovered (not recoverable)")
	}
}

// TestE2E_RetryAllFlow tests the bulk retry-all endpoint:
// 1. Ingest several entries
// 2. Call retry-all
// 3. Verify all recoverable entries were retried
func TestE2E_RetryAllFlow(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	ctx := context.Background()

	proc := NewProcessor(store)

	for i := 1; i <= 5; i++ {
		e := Entry{
			DLQID:           fmt.Sprintf("e2e-ra-%d", i),
			OriginalSubject: "swarm.task.request",
			OriginalPayload: json.RawMessage(fmt.Sprintf(`{"task_id":"t%d"}`, i)),
			Reason:          ReasonNoCapableAgent,
			FailedAt:        time.Now().UTC(),
			Source:          SourceDispatch,
			Recoverable:     i <= 3, // Only first 3 are recoverable.
		}
		d, _ := json.Marshal(e)
		proc.Process(ctx, "dlq.task.unassignable", d)
	}

	handler := NewHandler(store, nc)
	router := chi.NewRouter()
	router.Mount("/dlq", handler.Routes())

	req := httptest.NewRequest("POST", "/dlq/retry-all", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("retry-all returned %d", w.Code)
	}

	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)

	retried := int(body["retried"].(float64))
	total := int(body["total"].(float64))

	if total != 3 {
		t.Errorf("expected 3 total recoverable, got %d", total)
	}
	if retried != 3 {
		t.Errorf("expected 3 retried, got %d", retried)
	}

	// Verify 3 NATS messages.
	msgs := nc.published()
	if len(msgs) != 3 {
		t.Errorf("expected 3 NATS messages, got %d", len(msgs))
	}

	// All 3 should be recovered now.
	for i := 1; i <= 3; i++ {
		e, _ := store.Get(ctx, fmt.Sprintf("e2e-ra-%d", i))
		if !e.Recovered {
			t.Errorf("e2e-ra-%d should be recovered", i)
		}
	}

	// 4 and 5 should NOT be recovered.
	for i := 4; i <= 5; i++ {
		e, _ := store.Get(ctx, fmt.Sprintf("e2e-ra-%d", i))
		if e.Recovered {
			t.Errorf("e2e-ra-%d should NOT be recovered", i)
		}
	}
}
