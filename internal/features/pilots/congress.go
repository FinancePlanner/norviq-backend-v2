package pilots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"norviq-api/internal/platform/marketdata"
	"norviq-api/internal/store/pilotsdb"
)

// SourceIdentity is PilotSourceIdentity: who a source fetches for.
type SourceIdentity struct {
	Kind string
	// Chamber is `senate` or `house`; nil for funds.
	Chamber *string
	// BioguideID is the primary match for congress rows, e.g. `P000197`.
	BioguideID *string
	// Aliases are exact "First Last" spellings, used only when a row has no
	// bioguide id.
	Aliases []string
	Cik     *string
}

func identityOf(p pilotsdb.Pilot) SourceIdentity {
	return SourceIdentity{Kind: pilotKind(p.Kind), Chamber: p.Chamber, BioguideID: p.BioguideID, Aliases: p.NameAliases, Cik: p.Cik}
}

// DisclosureInput is PilotDisclosureInput: one disclosed trade or 13F
// holding. SourceKey is unique per pilot and stable across fetches.
type DisclosureInput struct {
	SourceKey       string
	Symbol          string
	Side            string
	Instrument      string
	TransactionDate *string
	DisclosureDate  *string
	AmountMin       *float64
	AmountMax       *float64
	Shares          *float64
	MarketValue     *float64
	Period          *string
}

// DisclosureSource is PilotDisclosureSource.
type DisclosureSource interface {
	Disclosures(ctx context.Context, pilot SourceIdentity) ([]DisclosureInput, error)
}

// DisabledSource is DisabledPilotSource: stands in for congress when FMP is
// not configured, so funds still ingest.
type DisabledSource struct{}

// Disclosures implements DisclosureSource.
func (DisabledSource) Disclosures(context.Context, SourceIdentity) ([]DisclosureInput, error) {
	return []DisclosureInput{}, nil
}

// CongressFeedLimit is FMPCongressPilotSource.feedLimit: the free plan
// returns the newest 25 rows per chamber.
const CongressFeedLimit = 25

// CongressFetch reads one chamber's latest feed ("senate" or "house").
type CongressFetch func(ctx context.Context, chamber string) ([]marketdata.FMPCongressTrade, error)

// CongressSource is FMPCongressPilotSource: congressional disclosures from
// FMP's free "latest" feeds, read once per chamber per run (the memo).
type CongressSource struct {
	fetch CongressFetch
	ttl   time.Duration
	now   func() time.Time

	mu    sync.Mutex
	cache map[string]*feedEntry
}

type feedEntry struct {
	at   time.Time
	done chan struct{}
	rows []marketdata.FMPCongressTrade
	err  error
}

// DefaultMemoTTL is FMPCongressPilotSource.defaultMemoTTL: one default
// ingestion interval, so a slow run never refetches a feed mid-run.
const DefaultMemoTTL = time.Duration(DefaultIngestionIntervalSeconds) * time.Second

// NewCongressSource builds a source with its own memo. ttl 0 is the default;
// now nil is time.Now.
func NewCongressSource(fetch CongressFetch, ttl time.Duration, now func() time.Time) *CongressSource {
	if ttl == 0 {
		ttl = DefaultMemoTTL
	}
	if now == nil {
		now = time.Now
	}
	return &CongressSource{fetch: fetch, ttl: ttl, now: now, cache: map[string]*feedEntry{}}
}

// rows is FeedMemo.rows: concurrent callers share one in-flight fetch, and a
// failed fetch is cached for the TTL too.
func (s *CongressSource) rows(ctx context.Context, chamber string) ([]marketdata.FMPCongressTrade, error) {
	s.mu.Lock()
	e, ok := s.cache[chamber]
	if !ok || !(s.now().Sub(e.at) < s.ttl) {
		e = &feedEntry{at: s.now(), done: make(chan struct{})}
		s.cache[chamber] = e
		go func() {
			defer close(e.done)
			e.rows, e.err = s.fetch(context.WithoutCancel(ctx), chamber)
		}()
	}
	s.mu.Unlock()
	select {
	case <-e.done:
		return e.rows, e.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Disclosures implements DisclosureSource.
func (s *CongressSource) Disclosures(ctx context.Context, pilot SourceIdentity) ([]DisclosureInput, error) {
	if pilot.Chamber == nil {
		return []DisclosureInput{}, nil
	}
	chamber := *pilot.Chamber
	rows, err := s.rows(ctx, chamber)
	if err != nil {
		return nil, err
	}
	aliases := map[string]bool{}
	for _, a := range pilot.Aliases {
		aliases[NormalizedName(a)] = true
	}
	// Ordinals are assigned over the whole chamber feed, in feed order,
	// before filtering, so they do not depend on which pilot asks.
	occurrences := map[string]int{}
	out := []DisclosureInput{}
	for i := range rows {
		wire := rows[i]
		material := KeyMaterial(wire, chamber)
		ordinal := occurrences[material]
		occurrences[material] = ordinal + 1
		if !CongressMatches(wire, pilot.BioguideID, aliases) {
			continue
		}
		if in := CongressInput(wire, chamber, ordinal); in != nil {
			out = append(out, *in)
		}
	}
	return out, nil
}

// trimWhitespaces is trimmingCharacters(in: .whitespaces): spaces and tabs,
// not newlines.
func trimWhitespaces(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r == '\t' || unicode.Is(unicode.Zs, r) })
}

// CongressMatches is FMPCongressPilotSource.matches: the bioguide id when the
// row has one, otherwise an exact "First Last" alias.
func CongressMatches(wire marketdata.FMPCongressTrade, bioguideID *string, aliases map[string]bool) bool {
	if wire.SenateID != nil {
		if id := trimWhitespaces(*wire.SenateID); id != "" {
			return strings.EqualFold(id, deref(bioguideID))
		}
	}
	return aliases[NormalizedName(deref(wire.FirstName)+" "+deref(wire.LastName))]
}

// KeyMaterial is FMPCongressPilotSource.keyMaterial.
func KeyMaterial(wire marketdata.FMPCongressTrade, chamber string) string {
	symbol := strings.ToUpper(strings.TrimSpace(deref(wire.Symbol)))
	parts := []string{
		chamber, deref(wire.SenateID), deref(wire.FirstName), deref(wire.LastName), deref(wire.Owner),
		symbol, deref(wire.TransactionDate), deref(wire.Type), deref(wire.Amount), deref(wire.Link),
		deref(wire.AssetType), deref(wire.AssetDescription), deref(wire.DisclosureDate),
	}
	return strings.Join(parts, "|")
}

// CongressInput is FMPCongressPilotSource.input(from:chamber:ordinal:).
func CongressInput(wire marketdata.FMPCongressTrade, chamber string, ordinal int) *DisclosureInput {
	if wire.Symbol == nil {
		return nil
	}
	symbol := strings.ToUpper(strings.TrimSpace(*wire.Symbol))
	if symbol == "" {
		return nil
	}
	side := CongressSide(wire.Type)
	if side == "" {
		return nil
	}
	instrument := CongressInstrument(wire.AssetType, wire.AssetDescription)
	if instrument == "" {
		return nil
	}
	lo, hi := marketdata.CongressAmountBounds(deref(wire.Amount))
	// The first occurrence keeps the bare material; repeats get "|#n".
	material := KeyMaterial(wire, chamber)
	if ordinal > 0 {
		material += "|#" + strconv.Itoa(ordinal)
	}
	sum := sha256.Sum256([]byte(material))
	return &DisclosureInput{
		SourceKey:       hex.EncodeToString(sum[:]),
		Symbol:          symbol,
		Side:            side,
		Instrument:      instrument,
		TransactionDate: wire.TransactionDate,
		DisclosureDate:  wire.DisclosureDate,
		AmountMin:       lo,
		AmountMax:       hi,
	}
}

// CongressSide is FMPCongressPilotSource.side(from:): purchase is a buy,
// "Sale (Full)" a full sale, any other sale a sale; anything else ("").
func CongressSide(raw *string) string {
	if raw == nil {
		return ""
	}
	lowered := strings.ToLower(*raw)
	if strings.Contains(lowered, "purchase") {
		return SideBuy
	}
	if strings.Contains(lowered, "sale") {
		if strings.Contains(lowered, "full") {
			return SideSellFull
		}
		return SideSell
	}
	return ""
}

var (
	putWord  = regexp.MustCompile(`(?i)\bputs?\b`)
	callWord = regexp.MustCompile(`(?i)\bcalls?\b`)
)

// CongressInstrument is FMPCongressPilotSource.instrument(assetType:
// description:); "" when the row is dropped.
func CongressInstrument(assetType, description *string) string {
	t := strings.ToLower(deref(assetType))
	if strings.Contains(t, "option") {
		text := deref(description)
		if putWord.MatchString(text) {
			return InstrumentPut
		}
		if callWord.MatchString(text) {
			return InstrumentCall
		}
		return ""
	}
	if t == "" || t == "stock" || strings.Contains(t, "etf") || strings.Contains(t, "equity") || strings.Contains(t, "common") {
		return InstrumentStock
	}
	return ""
}

// NormalizedName is FMPCongressPilotSource.normalizedName.
func NormalizedName(raw string) string {
	return strings.Join(strings.Fields(strings.ToLower(raw)), " ")
}
