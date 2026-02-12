-- DLQ: Dead Letter Queue table
-- Apply to swarm Supabase project

create table if not exists swarm_dlq (
  dlq_id           uuid primary key default gen_random_uuid(),
  original_subject text not null,
  original_payload jsonb not null,
  reason           text not null,
  reason_detail    text,
  failed_at        timestamptz not null default now(),
  retry_count      int not null default 0,
  max_retries      int not null default 3,
  retry_history    jsonb default '[]',
  source           text not null,
  recoverable      boolean default true,
  recovered        boolean default false,
  recovered_at     timestamptz,
  recovered_by     text,
  created_at       timestamptz default now()
);

create index if not exists idx_dlq_reason     on swarm_dlq (reason);
create index if not exists idx_dlq_source     on swarm_dlq (source);
create index if not exists idx_dlq_recovered  on swarm_dlq (recovered);
create index if not exists idx_dlq_failed_at  on swarm_dlq (failed_at desc);

-- Composite for recovery scanner: unrecovered + recent
create index if not exists idx_dlq_recovery on swarm_dlq (recoverable, recovered, failed_at desc)
  where recoverable = true and recovered = false;
