-- name: RecordFileRead :exec
INSERT INTO read_files (
    session_id,
    path,
    read_at
) VALUES (
    ?,
    ?,
    strftime('%s', 'now')
) ON CONFLICT(path, session_id) DO UPDATE SET
    read_at = excluded.read_at;

-- name: GetFileRead :one
SELECT * FROM read_files
WHERE session_id = ? AND path = ? LIMIT 1;

-- name: ListSessionReadFiles :many
SELECT * FROM read_files
WHERE session_id = ?
ORDER BY read_at DESC;

-- name: CopySessionReadFiles :exec
-- The files read in one session, recorded as read at the same times in
-- another, as /branch copies a conversation.
INSERT INTO read_files (session_id, path, read_at)
SELECT CAST(sqlc.arg(to_session_id) AS TEXT), r.path, r.read_at
FROM read_files AS r
WHERE r.session_id = sqlc.arg(from_session_id);

-- name: DeleteSessionReadFilesSince :exec
-- Forgets the reads of a session made at or after a time, as /rewind
-- forgets those of the conversation it cuts.
DELETE FROM read_files
WHERE session_id = ? AND read_at >= ?;
