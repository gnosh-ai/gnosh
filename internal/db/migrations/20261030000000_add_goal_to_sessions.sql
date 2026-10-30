-- +goose Up
-- The session's /goal: its binding to a goal in mem, as JSON (gnovm's
-- GoalBinding), "" for none. Only its own query writes it.
ALTER TABLE sessions ADD COLUMN goal TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN goal;
