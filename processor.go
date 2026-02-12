package dlq

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
)

// Processor handles incoming DLQ NATS messages and persists them to swarm_dlq.
// This is used by Chronicle: on any dlq.> event, call Process() to write to the
// structured DLQ table in addition to the raw swarm_events log.
type Processor struct {
	store DataStore
}

// NewProcessor creates a DLQ processor for Chronicle integration.
func NewProcessor(store DataStore) *Processor {
	return &Processor{store: store}
}

// Process parses a raw DLQ event payload and inserts it into swarm_dlq.
// subject is the NATS subject (e.g. "dlq.task.unassignable").
func (p *Processor) Process(ctx context.Context, subject string, data []byte) {
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		slog.Warn("dlq processor: malformed dlq event",
			"subject", subject,
			"error", err,
		)
		return
	}

	// Fill in defaults if publisher didn't set them.
	if entry.RetryHistory == nil {
		entry.RetryHistory = []RetryAttempt{}
	}
	if entry.Source == "" {
		entry.Source = inferSource(subject)
	}

	if err := p.store.Insert(ctx, entry); err != nil {
		slog.Error("dlq processor: failed to insert",
			"dlq_id", entry.DLQID,
			"subject", subject,
			"error", err,
		)
	}
}

func inferSource(subject string) string {
	if strings.HasPrefix(subject, "dlq.agent.") {
		return SourceWarren
	}
	return SourceDispatch
}
