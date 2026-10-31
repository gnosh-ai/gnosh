-- +goose Up
-- The frame in mem whose pop removed the message from the model's context
-- (FRAMES.md), "" for none. Every list built for the model skips a popped
-- message; every list built for the human keeps it. Only its own query
-- writes it.
ALTER TABLE messages ADD COLUMN popped TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE messages DROP COLUMN popped;
