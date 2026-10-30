//go:build ignore

// Hermes stub for parity/insights-admin.jsonl: serves the five Hermes
// endpoints with ids that parity/insights-setup.sql already seeded, so
// both backends' syncs dedupe to zero inserts and answer the same.
//
//	go run parity/insights-hermes-stub.go -addr 127.0.0.1:18137
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strings"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18137", "listen address")
	flag.Parse()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/finance/events", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"count": 2, "events": []any{
			map[string]any{
				"eventId": "parity-insights:e1", "source": "hermes", "sourceId": "s1", "topic": "parity-insights",
				"observedAt": "2026-10-29T10:00:00+00:00Z", "ingestedAt": "2026-10-29T10:00:00+00:00",
				"payload":   map[string]any{"title": "Parity rally", "summary": "Summary one", "url": "https://parity.test/1", "author": "author"},
				"sentiment": map[string]any{"label": "positive", "score": 0.6},
			},
			map[string]any{"eventId": "parity-insights:e2", "topic": "parity-insights", "observedAt": "2026-10-29T09:00:00+00:00"},
		}})
	})
	mux.HandleFunc("/finance/summary", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"windowDays": 30, "totalEvents": 2, "byTopic": map[string]int{"parity-insights": 2}})
	})
	mux.HandleFunc("/finance/sentiment", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{
			"topic": r.URL.Query().Get("topic"), "windowDays": 30, "count": 2,
			"labelCounts": map[string]int{"positive": 1, "negative": 1}, "averageScore": 0.1, "sampled": 2,
		})
	})
	mux.HandleFunc("/finance/net-worth", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{
			"latest": map[string]any{"eventId": "parity-insights:n2", "ingestedAt": "2026-10-29T10:00:00+00:00", "value": 1250.25},
			"history": []any{
				map[string]any{"eventId": "parity-insights:n1", "ingestedAt": "2026-10-27T10:00:00+00:00", "value": 1000.5},
				map[string]any{"eventId": "parity-insights:n2", "ingestedAt": "2026-10-29T10:00:00+00:00", "value": 1250.25},
			},
		})
	})
	mux.HandleFunc("/finance/ticker/", func(w http.ResponseWriter, r *http.Request) {
		symbol := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/finance/ticker/"), "/posts")
		posts := []any{}
		if symbol == "PRTYA" {
			posts = []any{
				map[string]any{
					"eventId": "parity-insights:p1", "author": "Trader", "authorHandle": "@trader", "text": "bullish breakout",
					"url": "https://parity.test/p1", "sentiment": "positive", "sentimentScore": 0.7, "confidence": 0.8,
					"postedAt": "2026-10-29T10:00:00+00:00",
				},
				map[string]any{"eventId": "parity-insights:p2", "text": "bearish crash", "sentiment": "negative", "sentimentScore": -0.5, "postedAt": "2026-10-29T09:00:00+00:00Z"},
				map[string]any{"eventId": "parity-insights:p3", "text": "loading calls", "postedAt": "2026-10-29T08:00:00Z"},
			}
		}
		write(w, map[string]any{"symbol": symbol, "days": 30, "count": len(posts), "posts": posts})
	})
	log.Fatal(http.ListenAndServe(*addr, mux)) //nolint:gosec // local parity stub
}
