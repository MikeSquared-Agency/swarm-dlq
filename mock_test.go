package dlq

import (
	"context"
	"fmt"
	"sync"
)

// mockStore is a thread-safe in-memory DataStore for unit tests.
type mockStore struct {
	mu      sync.Mutex
	entries map[string]*Entry

	insertErr   error
	getErr      error
	listErr     error
	recoverErr  error
	statsErr    error

	insertCalls  int
	recoverCalls int
}

func newMockStore() *mockStore {
	return &mockStore{entries: make(map[string]*Entry)}
}

func (m *mockStore) Insert(_ context.Context, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.insertCalls++
	if m.insertErr != nil {
		return m.insertErr
	}
	cp := e
	m.entries[e.DLQID] = &cp
	return nil
}

func (m *mockStore) Get(_ context.Context, dlqID string) (*Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	e, ok := m.entries[dlqID]
	if !ok {
		return nil, fmt.Errorf("not found: %s", dlqID)
	}
	cp := *e
	return &cp, nil
}

func (m *mockStore) List(_ context.Context, opts ListOpts) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	var result []Entry
	for _, e := range m.entries {
		if opts.Recovered != nil && e.Recovered != *opts.Recovered {
			continue
		}
		if opts.Reason != "" && e.Reason != opts.Reason {
			continue
		}
		if opts.Source != "" && e.Source != opts.Source {
			continue
		}
		result = append(result, *e)
		limit := opts.Limit
		if limit <= 0 {
			limit = 50
		}
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (m *mockStore) MarkRecovered(_ context.Context, dlqID, recoveredBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoverCalls++
	if m.recoverErr != nil {
		return m.recoverErr
	}
	e, ok := m.entries[dlqID]
	if !ok {
		return fmt.Errorf("not found: %s", dlqID)
	}
	if e.Recovered {
		return fmt.Errorf("already recovered: %s", dlqID)
	}
	e.Recovered = true
	e.RecoveredBy = recoveredBy
	return nil
}

func (m *mockStore) ListRecoverable(_ context.Context) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	var result []Entry
	for _, e := range m.entries {
		if e.Recoverable && !e.Recovered {
			result = append(result, *e)
		}
	}
	return result, nil
}

func (m *mockStore) Stats(_ context.Context) (*Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statsErr != nil {
		return nil, m.statsErr
	}
	s := &Stats{
		ByReason: make(map[string]int),
		BySource: make(map[string]int),
	}
	for _, e := range m.entries {
		s.Total++
		if !e.Recovered {
			s.Unrecovered++
			s.ByReason[e.Reason]++
			s.BySource[e.Source]++
			if e.Recoverable {
				s.Recoverable++
			}
		}
	}
	return s, nil
}

func (m *mockStore) seed(entries ...Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range entries {
		e := entries[i]
		if e.RetryHistory == nil {
			e.RetryHistory = []RetryAttempt{}
		}
		m.entries[e.DLQID] = &e
	}
}

// mockNATS captures published messages for test assertions.
type mockNATS struct {
	mu       sync.Mutex
	messages []publishedMsg
	err      error
}

type publishedMsg struct {
	Subject string
	Data    []byte
}

func newMockNATS() *mockNATS {
	return &mockNATS{}
}

func (m *mockNATS) Publish(subject string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.messages = append(m.messages, publishedMsg{Subject: subject, Data: data})
	return nil
}

func (m *mockNATS) published() []publishedMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]publishedMsg, len(m.messages))
	copy(cp, m.messages)
	return cp
}

// Verify interfaces at compile time.
var _ DataStore = (*mockStore)(nil)
var _ NATSPublisher = (*mockNATS)(nil)
