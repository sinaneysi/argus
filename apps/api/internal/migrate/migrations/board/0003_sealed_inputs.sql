-- Sealed inputs of the capability executor (ADR-0022 D4, PR 3). The api seals each secret value an admin types at
-- approval to actd's public key; the worker keeps the ciphertexts (JSON, "<step>/<NAME>" -> sealed) with the
-- approval and passes them to actd unopened. A column that the board sync already added is skipped.
ALTER TABLE agent_tasks ADD COLUMN sealed_inputs TEXT;
