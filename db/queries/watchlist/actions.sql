-- WatchlistService.list (the AI action catalog's list_watchlist): every
-- item the user has, across lists, by symbol.

-- name: ListAllWatchlistItemsBySymbol :many
SELECT * FROM watchlist_items WHERE user_id = $1 ORDER BY symbol ASC;
