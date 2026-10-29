-- Clearing learning history must not refund consumed shared-provider budget.
-- This ledger contains only opaque invocation IDs and timestamps, no messages,
-- identities, prompts or credentials. It is independent of task retention.
alter table ai_invocations drop constraint ai_invocations_task_id_fkey;
