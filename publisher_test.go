package dlq

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestPublisher_MarshalEntry(t *testing.T) {
	// Test that PublishOpts produces a valid Entry JSON.
	// We can't test actual NATS publish without a server, so test marshaling.
	opts := PublishOpts{
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task_id":"t1","title":"test"}`),
		Reason:          ReasonNoCapableAgent,
		ReasonDetail:    "No agent with capabilities [research]",
		RetryCount:      3,
		MaxRetries:      3,
		RetryHistory: []RetryAttempt{
			{Attempt: 1, AttemptedAt: time.Now().UTC(), Agent: "scout", FailureReason: "agent_unavailable"},
			{Attempt: 2, AttemptedAt: time.Now().UTC(), Agent: "kai", FailureReason: "boot_failure"},
			{Attempt: 3, AttemptedAt: time.Now().UTC(), FailureReason: "no_capable_agent"},
		},
		Recoverable: true,
	}

	// Simulate what Publisher.Publish does internally.
	entry := Entry{
		DLQID:           "test-id",
		OriginalSubject: opts.OriginalSubject,
		OriginalPayload: opts.OriginalPayload,
		Reason:          opts.Reason,
		ReasonDetail:    opts.ReasonDetail,
		FailedAt:        time.Now().UTC(),
		RetryCount:      opts.RetryCount,
		MaxRetries:      opts.MaxRetries,
		RetryHistory:    opts.RetryHistory,
		Source:          SourceDispatch,
		Recoverable:     opts.Recoverable,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// Verify round-trip.
	var decoded Entry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.DLQID != "test-id" {
		t.Errorf("expected dlq_id test-id, got %s", decoded.DLQID)
	}
	if decoded.Reason != ReasonNoCapableAgent {
		t.Errorf("expected reason %s, got %s", ReasonNoCapableAgent, decoded.Reason)
	}
	if len(decoded.RetryHistory) != 3 {
		t.Errorf("expected 3 retry attempts, got %d", len(decoded.RetryHistory))
	}
	if decoded.RetryHistory[2].Agent != "" {
		t.Errorf("expected empty agent on attempt 3, got %s", decoded.RetryHistory[2].Agent)
	}
	if !decoded.Recoverable {
		t.Error("expected recoverable to be true")
	}
}

func TestNewPublisher(t *testing.T) {
	// Verify constructor doesn't panic with nil conn (won't publish, but shouldn't crash on create).
	p := NewPublisher((*nats.Conn)(nil), SourceDispatch)
	if p.source != SourceDispatch {
		t.Errorf("expected source dispatch, got %s", p.source)
	}
}
