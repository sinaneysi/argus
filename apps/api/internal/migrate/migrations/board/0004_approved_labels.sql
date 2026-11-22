-- The board labels an admin approved with a start (ADR-0022 PR 5 review): the api signs their hash into the start
-- approval; the worker keeps the list (JSON) and passes it to actd in every request of the action. A column that the
-- board sync already added is skipped.
ALTER TABLE agent_tasks ADD COLUMN approved_labels TEXT;
