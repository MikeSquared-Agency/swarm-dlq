package dlq

import (
	"testing"
)

func TestSubjectForReason_TaskReasons(t *testing.T) {
	tests := []struct {
		reason   string
		expected string
	}{
		{ReasonNoCapableAgent, SubjectTaskUnassignable},
		{ReasonAllAgentsUnavailable, SubjectTaskNoAvailableAgent},
		{ReasonPolicyDenied, SubjectTaskPolicyDenied},
		{ReasonTimeoutAssigned, SubjectTaskAssignTimeout},
		{ReasonTimeoutInProgress, SubjectTaskExecTimeout},
		{ReasonAgentCrashed, SubjectTaskAgentCrashed},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			got := SubjectForReason(SourceDispatch, tt.reason)
			if got != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, got)
			}
		})
	}
}

func TestSubjectForReason_AgentReasons(t *testing.T) {
	tests := []struct {
		reason   string
		expected string
	}{
		{ReasonBootFailure, SubjectAgentBootFailure},
		{ReasonPullFailure, SubjectAgentPullFailure},
		{ReasonCrashLoop, SubjectAgentCrashLoop},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			got := SubjectForReason(SourceWarren, tt.reason)
			if got != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, got)
			}
		})
	}
}

func TestSubjectForReason_UnknownReason(t *testing.T) {
	if got := SubjectForReason(SourceDispatch, "unknown"); got != "dlq.task.unknown" {
		t.Errorf("expected dlq.task.unknown, got %s", got)
	}
	if got := SubjectForReason(SourceWarren, "unknown"); got != "dlq.agent.unknown" {
		t.Errorf("expected dlq.agent.unknown, got %s", got)
	}
}

func TestEntryDefaults(t *testing.T) {
	e := Entry{
		Reason: ReasonNoCapableAgent,
		Source: SourceDispatch,
	}

	if e.Recoverable != false {
		t.Error("expected default recoverable to be false (zero value)")
	}
	if e.Recovered != false {
		t.Error("expected default recovered to be false")
	}
}
