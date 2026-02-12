package dlq

import (
	"context"
	"log/slog"
	"time"
)

// Scanner periodically checks for recoverable DLQ entries and republishes them.
// This implements Phase 3 automated recovery from the spec.
type Scanner struct {
	store    DataStore
	nc       NATSPublisher
	interval time.Duration
	done     chan struct{}
}

// NewScanner creates a DLQ recovery scanner.
func NewScanner(store DataStore, nc NATSPublisher, interval time.Duration) *Scanner {
	return &Scanner{
		store:    store,
		nc:       nc,
		interval: interval,
		done:     make(chan struct{}),
	}
}

// Start begins the periodic scan loop. Call with a cancellable context for shutdown.
func (s *Scanner) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	go func() {
		defer ticker.Stop()
		defer close(s.done)
		for {
			select {
			case <-ticker.C:
				s.scan(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Wait blocks until the scanner has stopped.
func (s *Scanner) Wait() {
	<-s.done
}

func (s *Scanner) scan(ctx context.Context) {
	entries, err := s.store.ListRecoverable(ctx)
	if err != nil {
		slog.Error("dlq scanner: failed to list recoverable entries", "error", err)
		return
	}

	if len(entries) == 0 {
		return
	}

	slog.Info("dlq scanner: found recoverable entries", "count", len(entries))

	retried := 0
	for _, entry := range entries {
		if err := s.nc.Publish(entry.OriginalSubject, entry.OriginalPayload); err != nil {
			slog.Error("dlq scanner: failed to republish",
				"dlq_id", entry.DLQID,
				"subject", entry.OriginalSubject,
				"error", err,
			)
			continue
		}

		if err := s.store.MarkRecovered(ctx, entry.DLQID, "auto-scanner"); err != nil {
			slog.Error("dlq scanner: failed to mark recovered",
				"dlq_id", entry.DLQID,
				"error", err,
			)
			continue
		}

		retried++
		slog.Info("dlq scanner: retried entry",
			"dlq_id", entry.DLQID,
			"reason", entry.Reason,
			"original_subject", entry.OriginalSubject,
		)
	}

	if retried > 0 {
		slog.Info("dlq scanner: scan complete", "retried", retried, "total", len(entries))
	}
}
