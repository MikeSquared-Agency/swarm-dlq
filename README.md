# swarm-dlq

Shared Dead Letter Queue library for the swarm. Provides types, a NATS publisher, Postgres persistence, HTTP management API, and an automated recovery scanner.

This is not a standalone service — it's a Go library imported by Dispatch (publisher), Warren (publisher), and Chronicle (consumer/persistence/API).

## Architecture

```mermaid
graph LR
    subgraph Publishers
        D[Dispatch] -- "dlq.task.*" --> NATS
        W[Warren] -- "dlq.agent.*" --> NATS
    end

    subgraph NATS JetStream
        NATS[DLQ Stream<br/>dlq.>]
    end

    subgraph Chronicle
        ING[Ingester] --> PROC[DLQ Processor]
        PROC --> DB[(swarm_dlq)]
        PROC --> EVT[(swarm_events)]
        API[DLQ API] --> DB
        SCAN[Recovery Scanner] --> DB
        SCAN -- republish --> NATS
    end

    NATS --> ING
    SLACK[Slack Bridge] --> NATS
```

## DLQ Event Lifecycle

```mermaid
stateDiagram-v2
    [*] --> Retrying : task fails
    Retrying --> Retrying : attempt < max_retries
    Retrying --> DeadLettered : retries exhausted
    DeadLettered --> Recovered : manual retry / auto-scanner
    DeadLettered --> Discarded : manual discard
    Recovered --> [*]
    Discarded --> [*]
```

## Retry Strategy

```mermaid
flowchart TD
    FAIL[Task fails] --> A1[Attempt 1: immediate<br/>best-scoring agent]
    A1 -- fails --> A2[Attempt 2: 10s delay<br/>re-score all agents]
    A2 -- fails --> A3[Attempt 3: 30s delay<br/>broadened match 70%+]
    A3 -- fails --> DLQ[Dead letter to dlq.*]
    A1 -- succeeds --> DONE[Task completed]
    A2 -- succeeds --> DONE
    A3 -- succeeds --> DONE
    DLQ --> ALERT[Slack alert]
    DLQ --> PERSIST[Write to swarm_dlq]

    NOTE["retry_eligible: false<br/>→ skip to DLQ immediately"]
    style NOTE fill:none,stroke:none
```

## Installation

```go
import dlq "github.com/DarlingtonDeveloper/swarm-dlq"
```

## Usage

### Publishing (Dispatch / Warren)

```go
pub := dlq.NewPublisher(natsConn, dlq.SourceDispatch)

err := pub.Publish(dlq.PublishOpts{
    OriginalSubject: "swarm.task.request",
    OriginalPayload: originalTaskJSON,
    Reason:          dlq.ReasonNoCapableAgent,
    ReasonDetail:    "No agent with capabilities [research, competitor-analysis]",
    RetryCount:      3,
    MaxRetries:      3,
    RetryHistory:    retryAttempts,
    Recoverable:     true,
})
```

### Consuming (Chronicle)

```go
// Create DLQ store from existing pgx pool.
dlqStore := dlq.NewStore(pool)

// Create processor for Chronicle's ingester.
dlqProc := dlq.NewProcessor(dlqStore)

// On every dlq.> message:
dlqProc.Process(ctx, msg.Subject(), msg.Data())
```

### HTTP API (Chronicle)

```go
dlqHandler := dlq.NewHandler(dlqStore, natsConn)

router.Mount("/api/v1/dlq", dlqHandler.Routes())
```

### Recovery Scanner

```go
scanner := dlq.NewScanner(dlqStore, natsConn, 5*time.Minute)
scanner.Start(ctx)
```

## API Endpoints

Mount under `/api/v1/dlq` on your router.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | List entries. Filter: `?recovered=false&reason=X&source=X&limit=N` |
| GET | `/stats` | Summary counts by reason and source |
| GET | `/{dlqID}` | Single entry with full payload and retry history |
| POST | `/{dlqID}/retry` | Republish original payload and mark recovered |
| POST | `/{dlqID}/discard` | Mark as discarded without retrying |
| POST | `/retry-all` | Retry all recoverable entries (last 24h) |

## DLQ Reasons

### From Dispatch (`dlq.task.*`)

| Reason | Subject | Trigger |
|--------|---------|---------|
| `no_capable_agent` | `dlq.task.unassignable` | No agent has required capabilities |
| `all_agents_unavailable` | `dlq.task.no_available_agent` | Capable agents exist but all busy/sleeping |
| `policy_denied` | `dlq.task.policy_denied` | Alexandria denies access |
| `timeout_assigned` | `dlq.task.assignment_timeout` | Agent never started the task |
| `timeout_in_progress` | `dlq.task.execution_timeout` | Task never completed |
| `agent_crashed` | `dlq.task.agent_crashed` | Agent died mid-task |

### From Warren (`dlq.agent.*`)

| Reason | Subject | Trigger |
|--------|---------|---------|
| `boot_failure` | `dlq.agent.boot_failure` | 3 consecutive health check failures |
| `pull_failure` | `dlq.agent.pull_failure` | Failed to pull soul/auth during boot |
| `crash_loop` | `dlq.agent.crash_loop` | 5+ restarts in 10 minutes |

## Database

```sql
-- See migrations/001_swarm_dlq.sql
```

## Testing

```bash
# Unit tests
go test ./...

# Integration tests (requires Postgres with swarm_dlq table)
DATABASE_URL=... go test ./... -v
```
