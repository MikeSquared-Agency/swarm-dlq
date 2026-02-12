package dlq

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestScanner_Scan_RecoverableEntries(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	store.seed(
		Entry{DLQID: "sc-1", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{"t":"1"}`), Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recoverable: true},
		Entry{DLQID: "sc-2", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{"t":"2"}`), Reason: ReasonAllAgentsUnavailable, Source: SourceDispatch, Recoverable: true},
	)

	scanner := NewScanner(store, nc, time.Minute)
	scanner.scan(context.Background())

	msgs := nc.published()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 published messages, got %d", len(msgs))
	}

	// Both should be marked recovered.
	e1, _ := store.Get(context.Background(), "sc-1")
	e2, _ := store.Get(context.Background(), "sc-2")
	if !e1.Recovered {
		t.Error("sc-1 should be recovered")
	}
	if !e2.Recovered {
		t.Error("sc-2 should be recovered")
	}
	if e1.RecoveredBy != "auto-scanner" {
		t.Errorf("expected recovered_by auto-scanner, got %s", e1.RecoveredBy)
	}
}

func TestScanner_Scan_NoRecoverableEntries(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	store.seed(
		Entry{DLQID: "sc-3", Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recoverable: false},
		Entry{DLQID: "sc-4", Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recoverable: true, Recovered: true},
	)

	scanner := NewScanner(store, nc, time.Minute)
	scanner.scan(context.Background())

	msgs := nc.published()
	if len(msgs) != 0 {
		t.Errorf("expected 0 published messages for non-recoverable entries, got %d", len(msgs))
	}
}

func TestScanner_Scan_NATSPublishError(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	nc.err = fmt.Errorf("nats connection lost")
	store.seed(
		Entry{DLQID: "sc-5", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{}`), Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recoverable: true},
	)

	scanner := NewScanner(store, nc, time.Minute)
	scanner.scan(context.Background())

	// Entry should NOT be marked recovered if NATS publish failed.
	e, _ := store.Get(context.Background(), "sc-5")
	if e.Recovered {
		t.Error("entry should not be recovered when NATS publish fails")
	}
}

func TestScanner_Scan_MarkRecoveredError(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	store.recoverErr = fmt.Errorf("db error on mark recovered")
	store.seed(
		Entry{DLQID: "sc-6", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{}`), Reason: ReasonNoCapableAgent, Source: SourceDispatch, Recoverable: true},
	)

	scanner := NewScanner(store, nc, time.Minute)
	scanner.scan(context.Background())

	// NATS publish should still happen.
	msgs := nc.published()
	if len(msgs) != 1 {
		t.Errorf("expected 1 published message, got %d", len(msgs))
	}

	// But recovery count won't increment since MarkRecovered fails.
	if store.recoverCalls != 1 {
		t.Errorf("expected 1 recover call, got %d", store.recoverCalls)
	}
}

func TestScanner_Scan_ListError(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	store.listErr = fmt.Errorf("db list failed")

	scanner := NewScanner(store, nc, time.Minute)
	// Should not panic.
	scanner.scan(context.Background())

	msgs := nc.published()
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages when list fails, got %d", len(msgs))
	}
}

func TestScanner_StartStop(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()

	scanner := NewScanner(store, nc, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	scanner.Start(ctx)

	// Let it tick a couple of times.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Wait should return after cancel.
	done := make(chan struct{})
	go func() {
		scanner.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("scanner did not stop within 2 seconds")
	}
}

func TestScanner_Scan_PublishesToCorrectSubject(t *testing.T) {
	store := newMockStore()
	nc := newMockNATS()
	store.seed(
		Entry{
			DLQID:           "sc-7",
			OriginalSubject: "swarm.agent.heartbeat",
			OriginalPayload: json.RawMessage(`{"agent":"scout"}`),
			Reason:          ReasonBootFailure,
			Source:          SourceWarren,
			Recoverable:     true,
		},
	)

	scanner := NewScanner(store, nc, time.Minute)
	scanner.scan(context.Background())

	msgs := nc.published()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// Scanner republishes to the original subject.
	if msgs[0].Subject != "swarm.agent.heartbeat" {
		t.Errorf("expected subject swarm.agent.heartbeat, got %s", msgs[0].Subject)
	}
}
