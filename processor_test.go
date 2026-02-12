package dlq

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestInferSource(t *testing.T) {
	tests := []struct {
		subject  string
		expected string
	}{
		{"dlq.task.unassignable", SourceDispatch},
		{"dlq.task.no_available_agent", SourceDispatch},
		{"dlq.agent.boot_failure", SourceWarren},
		{"dlq.agent.crash_loop", SourceWarren},
		{"dlq.unknown", SourceDispatch},
	}

	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			got := inferSource(tt.subject)
			if got != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, got)
			}
		})
	}
}

func TestProcessor_Process_ValidEntry(t *testing.T) {
	store := newMockStore()
	proc := NewProcessor(store)

	entry := Entry{
		DLQID:           "proc-1",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task_id":"t1"}`),
		Reason:          ReasonNoCapableAgent,
		ReasonDetail:    "No agent with capabilities [research]",
		FailedAt:        time.Now().UTC(),
		RetryCount:      3,
		MaxRetries:      3,
		RetryHistory: []RetryAttempt{
			{Attempt: 1, AttemptedAt: time.Now().UTC(), Agent: "scout", FailureReason: "unavailable"},
		},
		Source:      SourceDispatch,
		Recoverable: true,
	}

	data, _ := json.Marshal(entry)
	proc.Process(context.Background(), "dlq.task.unassignable", data)

	if store.insertCalls != 1 {
		t.Fatalf("expected 1 insert call, got %d", store.insertCalls)
	}

	stored, err := store.Get(context.Background(), "proc-1")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if stored.DLQID != "proc-1" {
		t.Errorf("expected dlq_id proc-1, got %s", stored.DLQID)
	}
	if stored.Reason != ReasonNoCapableAgent {
		t.Errorf("expected reason %s, got %s", ReasonNoCapableAgent, stored.Reason)
	}
	if stored.Source != SourceDispatch {
		t.Errorf("expected source dispatch, got %s", stored.Source)
	}
}

func TestProcessor_Process_InfersSource(t *testing.T) {
	store := newMockStore()
	proc := NewProcessor(store)

	// Entry without Source — should be inferred from subject.
	entry := Entry{
		DLQID:           "proc-2",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{}`),
		Reason:          ReasonBootFailure,
		FailedAt:        time.Now().UTC(),
	}

	data, _ := json.Marshal(entry)
	proc.Process(context.Background(), "dlq.agent.boot_failure", data)

	stored, err := store.Get(context.Background(), "proc-2")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if stored.Source != SourceWarren {
		t.Errorf("expected inferred source warren, got %s", stored.Source)
	}
}

func TestProcessor_Process_NilRetryHistory(t *testing.T) {
	store := newMockStore()
	proc := NewProcessor(store)

	// Entry with null retry_history — should default to [].
	data := []byte(`{
		"dlq_id": "proc-3",
		"original_subject": "swarm.task.request",
		"original_payload": {},
		"reason": "no_capable_agent",
		"failed_at": "2025-01-01T00:00:00Z",
		"source": "dispatch"
	}`)

	proc.Process(context.Background(), "dlq.task.unassignable", data)

	stored, err := store.Get(context.Background(), "proc-3")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if stored.RetryHistory == nil {
		t.Error("expected non-nil retry_history")
	}
	if len(stored.RetryHistory) != 0 {
		t.Errorf("expected empty retry_history, got %d items", len(stored.RetryHistory))
	}
}

func TestProcessor_Process_MalformedJSON(t *testing.T) {
	store := newMockStore()
	proc := NewProcessor(store)

	// Invalid JSON — should log warning and not insert.
	proc.Process(context.Background(), "dlq.task.unassignable", []byte("not json"))

	if store.insertCalls != 0 {
		t.Errorf("expected 0 insert calls for malformed JSON, got %d", store.insertCalls)
	}
}

func TestProcessor_Process_InsertError(t *testing.T) {
	store := newMockStore()
	store.insertErr = fmt.Errorf("db write failed")
	proc := NewProcessor(store)

	entry := Entry{
		DLQID:           "proc-4",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{}`),
		Reason:          ReasonNoCapableAgent,
		FailedAt:        time.Now().UTC(),
		Source:          SourceDispatch,
	}

	data, _ := json.Marshal(entry)
	// Should not panic even when insert fails.
	proc.Process(context.Background(), "dlq.task.unassignable", data)

	if store.insertCalls != 1 {
		t.Errorf("expected 1 insert call (even if it fails), got %d", store.insertCalls)
	}
}

func TestProcessor_Process_PreservesExistingSource(t *testing.T) {
	store := newMockStore()
	proc := NewProcessor(store)

	// Entry already has Source set — should NOT override with inferred.
	entry := Entry{
		DLQID:           "proc-5",
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{}`),
		Reason:          ReasonBootFailure,
		FailedAt:        time.Now().UTC(),
		Source:          SourceDispatch, // explicitly set dispatch even on agent subject
	}

	data, _ := json.Marshal(entry)
	proc.Process(context.Background(), "dlq.agent.boot_failure", data)

	stored, _ := store.Get(context.Background(), "proc-5")
	if stored.Source != SourceDispatch {
		t.Errorf("expected preserved source dispatch, got %s", stored.Source)
	}
}
