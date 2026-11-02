-- name: UnlockCryptolalia :execrows
INSERT INTO commander_cryptolalia (commander_id, story_id) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: ListCryptolalia :many
SELECT story_id FROM commander_cryptolalia WHERE commander_id = ? ORDER BY story_id;

-- name: LockCryptolalia :execrows
DELETE FROM commander_cryptolalia WHERE commander_id = ? AND story_id = ?;
