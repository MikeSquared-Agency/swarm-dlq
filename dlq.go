// Package dlq provides the shared Dead Letter Queue types and constants
// used across the swarm (Dispatch, Warren, Chronicle).
package dlq

import (
	"encoding/json"
	"time"
)

// Reasons a task or agent operation can be dead-lettered.
const (
	ReasonNoCapableAgent      = "no_capable_agent"
	ReasonAllAgentsUnavailable = "all_agents_unavailable"
	ReasonPolicyDenied        = "policy_denied"
	ReasonTimeoutAssigned     = "timeout_assigned"
	ReasonTimeoutInProgress   = "timeout_in_progress"
	ReasonAgentCrashed        = "agent_crashed"
	ReasonBootFailure         = "boot_failure"
	ReasonHealthCheckFailed   = "health_check_failed"
	ReasonPullFailure         = "pull_failure"
	ReasonCrashLoop           = "crash_loop"
)

// Sources that publish DLQ events.
const (
	SourceDispatch = "dispatch"
	SourceWarren   = "warren"
)

// NATS subjects for DLQ events.
const (
	SubjectTaskUnassignable    = "dlq.task.unassignable"
	SubjectTaskNoAvailableAgent = "dlq.task.no_available_agent"
	SubjectTaskPolicyDenied    = "dlq.task.policy_denied"
	SubjectTaskAssignTimeout   = "dlq.task.assignment_timeout"
	SubjectTaskExecTimeout     = "dlq.task.execution_timeout"
	SubjectTaskAgentCrashed    = "dlq.task.agent_crashed"
	SubjectAgentBootFailure    = "dlq.agent.boot_failure"
	SubjectAgentPullFailure    = "dlq.agent.pull_failure"
	SubjectAgentCrashLoop      = "dlq.agent.crash_loop"
)

// Entry is a dead-lettered item.
type Entry struct {
	DLQID           string          `json:"dlq_id"`
	OriginalSubject string          `json:"original_subject"`
	OriginalPayload json.RawMessage `json:"original_payload"`
	Reason          string          `json:"reason"`
	ReasonDetail    string          `json:"reason_detail,omitempty"`
	FailedAt        time.Time       `json:"failed_at"`
	RetryCount      int             `json:"retry_count"`
	MaxRetries      int             `json:"max_retries"`
	RetryHistory    []RetryAttempt  `json:"retry_history"`
	Source          string          `json:"source"`
	Recoverable     bool            `json:"recoverable"`
	Recovered       bool            `json:"recovered"`
	RecoveredAt     *time.Time      `json:"recovered_at,omitempty"`
	RecoveredBy     string          `json:"recovered_by,omitempty"`
}

// RetryAttempt records one retry attempt before dead-lettering.
type RetryAttempt struct {
	Attempt       int       `json:"attempt"`
	AttemptedAt   time.Time `json:"attempted_at"`
	Agent         string    `json:"agent,omitempty"`
	FailureReason string    `json:"failure_reason"`
}

// SubjectForReason returns the NATS subject to publish to for a given reason and source.
func SubjectForReason(source, reason string) string {
	switch reason {
	case ReasonNoCapableAgent:
		return SubjectTaskUnassignable
	case ReasonAllAgentsUnavailable:
		return SubjectTaskNoAvailableAgent
	case ReasonPolicyDenied:
		return SubjectTaskPolicyDenied
	case ReasonTimeoutAssigned:
		return SubjectTaskAssignTimeout
	case ReasonTimeoutInProgress:
		return SubjectTaskExecTimeout
	case ReasonAgentCrashed:
		return SubjectTaskAgentCrashed
	case ReasonBootFailure:
		return SubjectAgentBootFailure
	case ReasonPullFailure:
		return SubjectAgentPullFailure
	case ReasonCrashLoop:
		return SubjectAgentCrashLoop
	default:
		if source == SourceWarren {
			return "dlq.agent.unknown"
		}
		return "dlq.task.unknown"
	}
}
