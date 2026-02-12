package dlq

import (
	"testing"
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
