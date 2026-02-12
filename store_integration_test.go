package dlq

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func skipWithoutDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set, skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func TestIntegration_InsertAndGet(t *testing.T) {
	pool := skipWithoutDB(t)
	s := NewStore(pool)
	ctx := context.Background()

	entry := Entry{
		DLQID:           "int-test-" + time.Now().Format("150405.000"),
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{"task_id":"t1","title":"integration test"}`),
		Reason:          ReasonNoCapableAgent,
		ReasonDetail:    "No agent with capabilities [test]",
		FailedAt:        time.Now().UTC(),
		RetryCount:      3,
		MaxRetries:      3,
		RetryHistory: []RetryAttempt{
			{Attempt: 1, AttemptedAt: time.Now().UTC(), Agent: "scout", FailureReason: "unavailable"},
		},
		Source:      SourceDispatch,
		Recoverable: true,
	}

	if err := s.Insert(ctx, entry); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.Get(ctx, entry.DLQID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Reason != ReasonNoCapableAgent {
		t.Errorf("expected reason %s, got %s", ReasonNoCapableAgent, got.Reason)
	}
	if got.Recovered {
		t.Error("expected not recovered")
	}
	if len(got.RetryHistory) != 1 {
		t.Errorf("expected 1 retry, got %d", len(got.RetryHistory))
	}

	// Cleanup.
	pool.Exec(ctx, "DELETE FROM swarm_dlq WHERE dlq_id = $1", entry.DLQID)
}

func TestIntegration_ListAndFilter(t *testing.T) {
	pool := skipWithoutDB(t)
	s := NewStore(pool)
	ctx := context.Background()

	prefix := "int-list-" + time.Now().Format("150405")

	entries := []Entry{
		{DLQID: prefix + "-a", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{}`), Reason: ReasonNoCapableAgent, Source: SourceDispatch, FailedAt: time.Now().UTC(), Recoverable: true},
		{DLQID: prefix + "-b", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{}`), Reason: ReasonBootFailure, Source: SourceWarren, FailedAt: time.Now().UTC(), Recoverable: true},
	}

	for _, e := range entries {
		if err := s.Insert(ctx, e); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// List all unrecovered.
	notRecovered := false
	all, err := s.List(ctx, ListOpts{Recovered: &notRecovered, Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected at least 2 entries, got %d", len(all))
	}

	// Filter by source.
	warren, err := s.List(ctx, ListOpts{Source: SourceWarren, Limit: 100})
	if err != nil {
		t.Fatalf("list by source: %v", err)
	}
	for _, e := range warren {
		if e.Source != SourceWarren {
			t.Errorf("expected source warren, got %s", e.Source)
		}
	}

	// Cleanup.
	for _, e := range entries {
		pool.Exec(ctx, "DELETE FROM swarm_dlq WHERE dlq_id = $1", e.DLQID)
	}
}

func TestIntegration_MarkRecovered(t *testing.T) {
	pool := skipWithoutDB(t)
	s := NewStore(pool)
	ctx := context.Background()

	id := "int-recover-" + time.Now().Format("150405.000")
	entry := Entry{
		DLQID:           id,
		OriginalSubject: "swarm.task.request",
		OriginalPayload: json.RawMessage(`{}`),
		Reason:          ReasonNoCapableAgent,
		Source:          SourceDispatch,
		FailedAt:        time.Now().UTC(),
		Recoverable:     true,
	}
	s.Insert(ctx, entry)

	if err := s.MarkRecovered(ctx, id, "test-recovery"); err != nil {
		t.Fatalf("mark recovered: %v", err)
	}

	got, _ := s.Get(ctx, id)
	if !got.Recovered {
		t.Error("expected recovered = true")
	}
	if got.RecoveredBy != "test-recovery" {
		t.Errorf("expected recovered_by test-recovery, got %s", got.RecoveredBy)
	}

	// Double-recover should fail.
	if err := s.MarkRecovered(ctx, id, "again"); err == nil {
		t.Error("expected error on double recovery")
	}

	// Cleanup.
	pool.Exec(ctx, "DELETE FROM swarm_dlq WHERE dlq_id = $1", id)
}

func TestIntegration_ListRecoverable(t *testing.T) {
	pool := skipWithoutDB(t)
	s := NewStore(pool)
	ctx := context.Background()

	prefix := "int-recoverable-" + time.Now().Format("150405")

	// One recoverable, one not.
	s.Insert(ctx, Entry{DLQID: prefix + "-a", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{}`), Reason: ReasonNoCapableAgent, Source: SourceDispatch, FailedAt: time.Now().UTC(), Recoverable: true})
	s.Insert(ctx, Entry{DLQID: prefix + "-b", OriginalSubject: "swarm.task.request", OriginalPayload: json.RawMessage(`{}`), Reason: ReasonPolicyDenied, Source: SourceDispatch, FailedAt: time.Now().UTC(), Recoverable: false})

	entries, err := s.ListRecoverable(ctx)
	if err != nil {
		t.Fatalf("list recoverable: %v", err)
	}

	for _, e := range entries {
		if !e.Recoverable {
			t.Errorf("expected only recoverable entries, got %s", e.DLQID)
		}
		if e.Recovered {
			t.Errorf("expected only unrecovered entries, got %s", e.DLQID)
		}
	}

	// Cleanup.
	pool.Exec(ctx, "DELETE FROM swarm_dlq WHERE dlq_id LIKE $1", prefix+"%")
}

func TestIntegration_Stats(t *testing.T) {
	pool := skipWithoutDB(t)
	s := NewStore(pool)
	ctx := context.Background()

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	// Just check it doesn't error and returns valid structure.
	if stats.Total < 0 {
		t.Error("expected non-negative total")
	}
	if stats.ByReason == nil {
		t.Error("expected non-nil ByReason map")
	}
}
