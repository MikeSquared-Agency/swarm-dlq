package dlq

import "context"

// DataStore is the interface for DLQ persistence.
// The concrete implementation is *Store (pgx-backed).
type DataStore interface {
	Insert(ctx context.Context, e Entry) error
	Get(ctx context.Context, dlqID string) (*Entry, error)
	List(ctx context.Context, opts ListOpts) ([]Entry, error)
	MarkRecovered(ctx context.Context, dlqID, recoveredBy string) error
	ListRecoverable(ctx context.Context) ([]Entry, error)
	Stats(ctx context.Context) (*Stats, error)
}
