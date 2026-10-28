#!/bin/zsh
# Prints RevenueCat HMAC webhook parity cases signed for the current time.
# Both backends need REVENUECAT_HMAC_SECRET=parity-hmac-secret:
#   parity/billing-webhook-hmac.sh > /tmp/hmac.jsonl && go run ./cmd/parity -a <vapor> -b <v2> -cases /tmp/hmac.jsonl -ignore requestId
secret=parity-hmac-secret
ts=$(date +%s)
sig() { printf '%s' "$1.$2" | openssl dgst -sha256 -hmac "$secret" -r | cut -d' ' -f1; }
body='{"event":{"id":"pw-hmac-1","type":"TEST","app_user_id":"x"}}'
good="t=$ts,v1=$(sig $ts "$body")"
upper="t=$ts,v1=$(sig $ts "$body" | tr a-f A-F)"
old=$((ts - 400))
oldsig="t=$old,v1=$(sig $old "$body")"
future=$((ts + 299))
futsig="t=$future,v1=$(sig $future "$body")"
H='"X-RevenueCat-Webhook-Signature"'
case_() { echo "{\"name\":\"$1\",\"method\":\"POST\",\"path\":\"/webhooks/revenuecat\",\"body\":$body,\"headers\":{$2}}"; }
case_ hmac-missing-header '"Authorization":"parity-webhook-secret"'
case_ hmac-blank-header "$H:\"  \""
case_ hmac-malformed "$H:\"garbage\""
case_ hmac-no-v1 "$H:\"t=$ts\""
case_ hmac-bad-t "$H:\"t=abc,v1=00\""
case_ hmac-empty-v1 "$H:\"t=$ts,v1=\""
case_ hmac-stale "$H:\"$oldsig\""
case_ hmac-wrong-sig "$H:\"t=$ts,v1=deadbeef\""
case_ hmac-good "$H:\"$good\""
case_ hmac-good-uppercase-hex "$H:\"$upper\""
case_ hmac-good-spaces "$H:\" t = $ts , v1 = $(sig $ts "$body") \""
case_ hmac-good-future-edge "$H:\"$futsig\""
case_ hmac-later-bad-t-wins "$H:\"$good,t=zz\""
case_ hmac-dup "$H:\"$good\""
