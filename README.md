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

## NATS Subject Routing

```mermaid
flowchart LR
    subgraph Dispatch Reasons
        R1[no_capable_agent] --> S1[dlq.task.unassignable]
        R2[all_agents_unavailable] --> S2[dlq.task.no_available_agent]
        R3[policy_denied] --> S3[dlq.task.policy_denied]
        R4[timeout_assigned] --> S4[dlq.task.assignment_timeout]
        R5[timeout_in_progress] --> S5[dlq.task.execution_timeout]
        R6[agent_crashed] --> S6[dlq.task.agent_crashed]
    end

    subgraph Warren Reasons
        R7[boot_failure] --> S7[dlq.agent.boot_failure]
        R8[pull_failure] --> S8[dlq.agent.pull_failure]
        R9[crash_loop] --> S9[dlq.agent.crash_loop]
    end
```

## Data Model

```mermaid
erDiagram
    swarm_dlq {
        uuid dlq_id PK
        text original_subject
        jsonb original_payload
        text reason
        text reason_detail
        timestamptz failed_at
        int retry_count
        int max_retries
        jsonb retry_history
        text source
        boolean recoverable
        boolean recovered
        timestamptz recovered_at
        text recovered_by
    }
```

## Recovery Flow

```mermaid
sequenceDiagram
    participant API as DLQ API
    participant Store as swarm_dlq
    participant NATS as NATS JetStream
    participant Scanner as Recovery Scanner

    Note over Scanner: Every 5 minutes
    Scanner->>Store: ListRecoverable()
    Store-->>Scanner: entries (recoverable=true, recovered=false, <24h)
    loop Each entry
        Scanner->>NATS: Publish(original_subject, original_payload)
        Scanner->>Store: MarkRecovered(dlq_id, "auto-scanner")
    end

    Note over API: Manual retry via HTTP
    API->>Store: Get(dlq_id)
    Store-->>API: entry
    API->>NATS: Publish(original_subject, original_payload)
    API->>Store: MarkRecovered(dlq_id, "api-retry")
```

## Component Dependency

```mermaid
graph TD
    DLQ[dlq.go<br/>Types & Constants]
    PUB[publisher.go<br/>NATS Publisher]
    STORE[store.go<br/>Postgres Persistence]
    PROC[processor.go<br/>Chronicle Integration]
    HAND[handler.go<br/>HTTP API]
    SCAN[scanner.go<br/>Auto Recovery]
    IFACE[interface.go<br/>DataStore Interface]

    PUB --> DLQ
    STORE --> DLQ
    STORE --> IFACE
    PROC --> DLQ
    PROC --> IFACE
    HAND --> DLQ
    HAND --> IFACE
    SCAN --> DLQ
    SCAN --> IFACE
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
# Unit tests (40 tests)
go test ./... -v

# Integration tests (requires Postgres with swarm_dlq table)
DATABASE_URL=... go test ./... -v
```

### Test Coverage

| Package | Tests | Coverage |
|---------|-------|----------|
| `dlq_test.go` | 4 | Subject routing, entry defaults |
| `handler_test.go` | 20 | All 6 HTTP endpoints, error paths |
| `processor_test.go` | 7 | Process(), source inference, error paths |
| `scanner_test.go` | 7 | Scan recovery, start/stop lifecycle, error paths |
| `publisher_test.go` | 2 | Marshal round-trip, constructor |
| `e2e_test.go` | 4 | Full lifecycle, discard, scanner recovery, retry-all |
| `store_integration_test.go` | 5 | Insert, list, filter, recover, stats (requires DB) |
