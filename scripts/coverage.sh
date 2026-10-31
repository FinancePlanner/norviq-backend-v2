#!/usr/bin/env bash
# Route coverage: which of Vapor's routes v2 serves (Vapor is gone; an
# unported route is a 404).
#
#   scripts/coverage.sh            # summary per top-level prefix
#   scripts/coverage.sh --missing  # every unported route
#
# docs/vapor-routes.txt is `StockPlanBackend routes` from the Vapor build the
# migration targets, with production flags set. Vapor spells path parameters
# `:name`, as echo does, so the two lists compare by method and shape.
set -euo pipefail
cd "$(dirname "$0")/.."

norm() { sed -E 's#:[A-Za-z0-9_]+#:p#g; s#\*\*#*#g' | sort -u; }

vapor="$(norm < docs/vapor-routes.txt)"
# echo adds RouteNotFound catch-alls for group middleware; they are not v2
# routes.
v2="$(go run ./cmd/api routes 2>/dev/null | grep -v "^echo_route_not_found " | norm)"

ported="$(comm -12 <(echo "$vapor") <(echo "$v2"))"
missing="$(comm -23 <(echo "$vapor") <(echo "$v2"))"
extra="$(comm -13 <(echo "$vapor") <(echo "$v2"))"

total=$(echo "$vapor" | grep -c . || true)
done_=$(echo "$ported" | grep -c . || true)

if [[ "${1:-}" == "--missing" ]]; then
  echo "$missing"
  exit 0
fi

echo "ported ${done_}/${total} Vapor routes"
echo
echo "by prefix (ported/total):"
paste -d' ' <(echo "$vapor" | awk '{split($2,a,"/"); print "/"a[2]"/"a[3]}' | sort | uniq -c | awk '{print $2, $1}') /dev/null \
  | while read -r prefix total_p _; do
      p=$(echo "$ported" | awk -v pre="$prefix" '{split($2,a,"/"); if ("/"a[2]"/"a[3]==pre) n++} END {print n+0}')
      printf "  %-32s %3d/%-3d\n" "$prefix" "$p" "$total_p"
    done
if [[ -n "$extra" ]]; then
  echo
  echo "v2 routes Vapor does not have (should be none besides the probe):"
  echo "$extra" | sed 's/^/  /'
fi
