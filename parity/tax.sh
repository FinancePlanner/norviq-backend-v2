#!/usr/bin/env bash
# Parity suites for the tax scope: /v1/tax, /v1/watchlist, /v1/targets,
# /v1/holding-risk-profiles and /v1/financing. Each suite re-seeds its own
# fixtures first and uses its own ignore list, so they stay separate files.
#
# Usage (from the repo root, both backends against the same scratch DB):
#   parity/tax.sh [vapor-url] [v2-url]
# Defaults: Vapor http://127.0.0.1:8081, v2 http://127.0.0.1:18113.
# Start v2 with TAX_VALIDATED_JURISDICTIONS, SCENARIO_PLANNING_ENABLED and
# V2_JOBS unset to match the shared Vapor's defaults.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

A="${1:-http://127.0.0.1:8081}"
B="${2:-http://127.0.0.1:18113}"
DB="${PARITY_DB:-postgres://pg:pg@127.0.0.1:55499/norviq}"
status=0

seed() { PGOPTIONS=--client-min-messages=warning psql -q "$DB" -v ON_ERROR_STOP=1 "$@" >/dev/null || { echo "seed failed: $*"; exit 1; }; }
run() { # cases-file ignore-keys [headers]
	local extra=()
	[[ -n "${3:-}" ]] && extra=(-headers "$3")
	echo "== $1"
	go run ./cmd/parity -a "$A" -b "$B" -jwt-secret parity-secret \
		-cases "parity/$1" -ignore "$2" ${extra[@]+"${extra[@]}"} || status=1
}

seed -f parity/tax-watchlist-setup.sql
run tax-watchlist.jsonl requestId,id,createdAt,updatedAt

for suite in tax-financing.jsonl:requestId,generatedAt \
	tax-financing-writes.jsonl:requestId,id,createdAt,updatedAt \
	tax-financing-decode.jsonl:requestId,reason \
	tax-financing-raw.jsonl:requestId; do
	seed -f parity/tax-financing-setup.sql
	run "${suite%%:*}" "${suite#*:}"
done

seed -f parity/tax-core-setup.sql
run tax-core.jsonl requestId,id,createdAt,updatedAt,generatedAt

# Ready reports point at the fixture files by absolute path; the fixed mtime
# keeps ETag/Last-Modified stable across runs.
touch -t 202601010000.00 parity/tax-reports-fixture.csv parity/tax-reports-fixture.pdf
seed -v dir="$PWD/parity" -f parity/tax-reports-setup.sql
run tax-reports.jsonl requestId,id,createdAt \
	Content-Type,Content-Disposition,Content-Range,Accept-Ranges,ETag,Last-Modified

exit "$status"
