package dlq

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// Publisher sends dead-letter events to the DLQ NATS stream.
type Publisher struct {
	nc     *nats.Conn
	source string
}

// NewPublisher creates a DLQ publisher. Source should be "dispatch" or "warren".
func NewPublisher(nc *nats.Conn, source string) *Publisher {
	return &Publisher{nc: nc, source: source}
}

// PublishOpts configures a dead-letter event.
type PublishOpts struct {
	OriginalSubject string
	OriginalPayload json.RawMessage
	Reason          string
	ReasonDetail    string
	RetryCount      int
	MaxRetries      int
	RetryHistory    []RetryAttempt
	Recoverable     bool
}

// Publish sends a dead-letter event to the appropriate DLQ subject.
func (p *Publisher) Publish(opts PublishOpts) error {
	entry := Entry{
		DLQID:           uuid.New().String(),
		OriginalSubject: opts.OriginalSubject,
		OriginalPayload: opts.OriginalPayload,
		Reason:          opts.Reason,
		ReasonDetail:    opts.ReasonDetail,
		FailedAt:        time.Now().UTC(),
		RetryCount:      opts.RetryCount,
		MaxRetries:      opts.MaxRetries,
		RetryHistory:    opts.RetryHistory,
		Source:          p.source,
		Recoverable:     opts.Recoverable,
	}

	if entry.RetryHistory == nil {
		entry.RetryHistory = []RetryAttempt{}
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal dlq entry: %w", err)
	}

	subject := SubjectForReason(p.source, opts.Reason)
	if err := p.nc.Publish(subject, data); err != nil {
		return fmt.Errorf("publish to %s: %w", subject, err)
	}

	return nil
}
