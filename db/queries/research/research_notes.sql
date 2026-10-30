-- Research notes (StockController /v1/research).

-- name: ListResearchNotes :many
SELECT * FROM research_notes
WHERE user_id = $1
ORDER BY updated_at DESC, created_at DESC
LIMIT $2;

-- name: ListResearchNotesBySymbol :many
SELECT * FROM research_notes
WHERE user_id = $1 AND symbol = $2
ORDER BY updated_at DESC, created_at DESC
LIMIT $3;

-- name: OwnedResearchNote :one
SELECT * FROM research_notes WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: InsertResearchNote :exec
INSERT INTO research_notes (
    id, user_id, symbol, title, thesis, risks, catalysts, reference_links, created_at, updated_at
) VALUES (
    @id, @user_id, @symbol, @title, @thesis, @risks, @catalysts, @reference_links, @created_at, @updated_at
);

-- name: UpdateResearchNote :exec
UPDATE research_notes SET
    symbol = @symbol,
    title = @title,
    thesis = @thesis,
    risks = @risks,
    catalysts = @catalysts,
    reference_links = @reference_links,
    updated_at = @updated_at
WHERE id = @id;

-- name: DeleteResearchNote :exec
DELETE FROM research_notes WHERE id = $1;
