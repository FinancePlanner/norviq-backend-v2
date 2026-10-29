#!/bin/bash
# Seeds every expenses-domain fixture into the scratch DB and runs all the
# expenses parity suites. Usage (from the repo root):
#   parity/expenses-run.sh [vapor-url] [v2-url]
# DATABASE_URL defaults to the scratch database. Never point it at production.
set -u
A=${1:-http://127.0.0.1:8081}
B=${2:-http://127.0.0.1:18112}
DB=${DATABASE_URL:-postgres://pg:pg@127.0.0.1:55499/norviq}
BIN=$(mktemp -d)/parity
go build -o "$BIN" ./cmd/parity || exit 1
seed() { psql "$DB" -q -v ON_ERROR_STOP=1 -f "parity/$1" >/dev/null || { echo "seed $1 failed"; exit 1; }; }
run() {
	echo "== $1"
	args=(-a "$A" -b "$B" -jwt-secret parity-secret -cases "parity/$1.jsonl" -ignore "$2")
	[ -n "${3:-}" ] && args+=(-headers "$3")
	"$BIN" "${args[@]}"
}
for f in stats budgeting core import reporting; do seed "expenses-$f-setup.sql"; done
seed expenses-setup.sql
run expenses-stats requestId,generatedAt
run expenses-budgeting requestId,quoteAsOf
run expenses-budgeting-writes requestId,id,createdAt,updatedAt
run expenses-core requestId,id,createdAt,updatedAt,generatedAt,revision
run expenses requestId,generatedAt
# The signed-download cases need both backends to share
# REPORT_DOWNLOAD_SIGNING_SECRET, PUBLIC_API_BASE_URL and the storage root.
run expenses-reporting requestId,updatedAt Content-Type,Content-Disposition,Cache-Control
run expenses-reporting-create requestId,id,createdAt,updatedAt
run expenses-import-upload requestId,sessionId,expiresAt
seed expenses-import-sessions.sql
run expenses-import requestId
