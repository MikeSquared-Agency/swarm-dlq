package dlq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store handles DLQ persistence to Supabase/Postgres.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a DLQ store from an existing connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Insert writes a DLQ entry to the swarm_dlq table.
func (s *Store) Insert(ctx context.Context, e Entry) error {
	retryJSON, err := json.Marshal(e.RetryHistory)
	if err != nil {
		retryJSON = []byte("[]")
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO swarm_dlq
			(dlq_id, original_subject, original_payload, reason, reason_detail,
			 failed_at, retry_count, max_retries, retry_history, source, recoverable)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (dlq_id) DO NOTHING
	`,
		e.DLQID, e.OriginalSubject, e.OriginalPayload, e.Reason, e.ReasonDetail,
		e.FailedAt, e.RetryCount, e.MaxRetries, retryJSON, e.Source, e.Recoverable,
	)
	if err != nil {
		return fmt.Errorf("insert dlq entry: %w", err)
	}
	return nil
}

// Get retrieves a single DLQ entry by ID.
func (s *Store) Get(ctx context.Context, dlqID string) (*Entry, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT dlq_id, original_subject, original_payload, reason, reason_detail,
		       failed_at, retry_count, max_retries, retry_history, source,
		       recoverable, recovered, recovered_at, recovered_by
		FROM swarm_dlq WHERE dlq_id = $1
	`, dlqID)
	return scanEntry(row)
}

// ListOpts filters the DLQ list query.
type ListOpts struct {
	Recovered *bool
	Reason    string
	Source    string
	Limit     int
}

// List returns DLQ entries matching the given filters.
func (s *Store) List(ctx context.Context, opts ListOpts) ([]Entry, error) {
	q := `SELECT dlq_id, original_subject, original_payload, reason, reason_detail,
	             failed_at, retry_count, max_retries, retry_history, source,
	             recoverable, recovered, recovered_at, recovered_by
	      FROM swarm_dlq WHERE 1=1`
	args := []any{}
	n := 1

	if opts.Recovered != nil {
		q += fmt.Sprintf(` AND recovered = $%d`, n)
		args = append(args, *opts.Recovered)
		n++
	}
	if opts.Reason != "" {
		q += fmt.Sprintf(` AND reason = $%d`, n)
		args = append(args, opts.Reason)
		n++
	}
	if opts.Source != "" {
		q += fmt.Sprintf(` AND source = $%d`, n)
		args = append(args, opts.Source)
		n++
	}

	q += ` ORDER BY failed_at DESC`

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	q += fmt.Sprintf(` LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list dlq: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		e, err := scanEntryFromRows(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *e)
	}
	return entries, rows.Err()
}

// MarkRecovered marks a DLQ entry as recovered.
func (s *Store) MarkRecovered(ctx context.Context, dlqID, recoveredBy string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE swarm_dlq
		SET recovered = true, recovered_at = now(), recovered_by = $2
		WHERE dlq_id = $1 AND recovered = false
	`, dlqID, recoveredBy)
	if err != nil {
		return fmt.Errorf("mark recovered: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("dlq entry %s not found or already recovered", dlqID)
	}
	return nil
}

// ListRecoverable returns entries eligible for auto-recovery
// (recoverable, not recovered, failed within the last 24 hours).
func (s *Store) ListRecoverable(ctx context.Context) ([]Entry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT dlq_id, original_subject, original_payload, reason, reason_detail,
		       failed_at, retry_count, max_retries, retry_history, source,
		       recoverable, recovered, recovered_at, recovered_by
		FROM swarm_dlq
		WHERE recoverable = true
		  AND recovered = false
		  AND failed_at > now() - interval '24 hours'
		ORDER BY failed_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list recoverable: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		e, err := scanEntryFromRows(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *e)
	}
	return entries, rows.Err()
}

// Stats returns summary counts for the DLQ.
type Stats struct {
	Total       int            `json:"total"`
	Unrecovered int            `json:"unrecovered"`
	Recoverable int            `json:"recoverable"`
	ByReason    map[string]int `json:"by_reason"`
	BySource    map[string]int `json:"by_source"`
}

func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	st := &Stats{
		ByReason: make(map[string]int),
		BySource: make(map[string]int),
	}

	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM swarm_dlq`).Scan(&st.Total)
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM swarm_dlq WHERE recovered = false`).Scan(&st.Unrecovered)
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM swarm_dlq WHERE recoverable = true AND recovered = false`).Scan(&st.Recoverable)

	rows, err := s.pool.Query(ctx, `SELECT reason, count(*) FROM swarm_dlq WHERE recovered = false GROUP BY reason`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var reason string
			var count int
			if err := rows.Scan(&reason, &count); err != nil {
				continue
			}
			st.ByReason[reason] = count
		}
	}

	rows2, err := s.pool.Query(ctx, `SELECT source, count(*) FROM swarm_dlq WHERE recovered = false GROUP BY source`)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var source string
			var count int
			if err := rows2.Scan(&source, &count); err != nil {
				continue
			}
			st.BySource[source] = count
		}
	}

	return st, nil
}

func scanEntry(row pgx.Row) (*Entry, error) {
	var (
		e            Entry
		retryJSON    json.RawMessage
		reasonDetail *string
		recoveredAt  *time.Time
		recoveredBy  *string
	)
	err := row.Scan(
		&e.DLQID, &e.OriginalSubject, &e.OriginalPayload, &e.Reason, &reasonDetail,
		&e.FailedAt, &e.RetryCount, &e.MaxRetries, &retryJSON, &e.Source,
		&e.Recoverable, &e.Recovered, &recoveredAt, &recoveredBy,
	)
	if err != nil {
		return nil, err
	}
	if reasonDetail != nil {
		e.ReasonDetail = *reasonDetail
	}
	if recoveredAt != nil {
		e.RecoveredAt = recoveredAt
	}
	if recoveredBy != nil {
		e.RecoveredBy = *recoveredBy
	}
	_ = json.Unmarshal(retryJSON, &e.RetryHistory)
	if e.RetryHistory == nil {
		e.RetryHistory = []RetryAttempt{}
	}
	return &e, nil
}

func scanEntryFromRows(rows pgx.Rows) (*Entry, error) {
	var (
		e            Entry
		retryJSON    json.RawMessage
		reasonDetail *string
		recoveredAt  *time.Time
		recoveredBy  *string
	)
	err := rows.Scan(
		&e.DLQID, &e.OriginalSubject, &e.OriginalPayload, &e.Reason, &reasonDetail,
		&e.FailedAt, &e.RetryCount, &e.MaxRetries, &retryJSON, &e.Source,
		&e.Recoverable, &e.Recovered, &recoveredAt, &recoveredBy,
	)
	if err != nil {
		return nil, err
	}
	if reasonDetail != nil {
		e.ReasonDetail = *reasonDetail
	}
	if recoveredAt != nil {
		e.RecoveredAt = recoveredAt
	}
	if recoveredBy != nil {
		e.RecoveredBy = *recoveredBy
	}
	_ = json.Unmarshal(retryJSON, &e.RetryHistory)
	if e.RetryHistory == nil {
		e.RetryHistory = []RetryAttempt{}
	}
	return &e, nil
}
