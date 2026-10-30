package assistant

import (
	"context"
	"fmt"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/ai"
	"norviq-api/internal/store/assistantdb"

	"github.com/google/uuid"
)

// positionMemos is PositionMemoService.generate: gather the evidence pack,
// compute the mark, ask the writer for one JSON draft, guard it, seal and
// store the memo, and return its card.
type positionMemos struct {
	h        *handlers
	evidence *memoEvidence
}

var _ memoGenerator = (*positionMemos)(nil)

func newPositionMemos(h *handlers, market memoMarket, news memoNewsFunc) *positionMemos {
	if news == nil {
		news = archivedMemoNews(h.q)
	}
	return &positionMemos{h: h, evidence: &memoEvidence{market: market, news: news, q: h.q, logger: h.d.Logger, now: h.now}}
}

// Generate implements memoGenerator.
func (p *positionMemos) Generate(ctx context.Context, ask *memoAsk, userID, conversationID uuid.UUID, client ai.ChatClient) (MemoCard, error) {
	started := time.Now()
	pack, mark := p.evidence.build(ctx, ask, userID)
	messages, err := memoWriterMessages(&pack, &mark)
	if err != nil {
		return MemoCard{}, err
	}
	reply, err := client.Chat(ctx, messages, []ai.Tool{}, "json_object")
	if err != nil {
		return MemoCard{}, err
	}
	parsed, err := parseMemoDraft(reply.Content)
	if err != nil {
		return MemoCard{}, err
	}
	draft := applyMemoDraftGuard(parsed, &pack)
	sources := buildMemoSources(&pack, &mark)

	h := p.h
	params := assistantdb.InsertMemoParams{
		ID: uuid.New(), UserID: userID, ConversationID: &conversationID,
		AskedSymbol: pack.AskedSymbol, PrimarySymbol: pack.PrimarySymbol,
	}
	for _, f := range []struct {
		dst *[]byte
		v   any
	}{
		{&params.TitleEncrypted, draft.Title},
		{&params.MarkEncrypted, mark},
		{&params.SectionsEncrypted, draft.Sections},
		{&params.VerdictEncrypted, draft.Verdict},
		{&params.SourcesEncrypted, sources},
		{&params.EvidenceEncrypted, pack},
	} {
		if *f.dst, err = h.seal(f.v); err != nil {
			return MemoCard{}, err
		}
	}
	created := h.now()
	params.CreatedAt = &created
	if err := h.q.InsertMemo(ctx, params); err != nil {
		return MemoCard{}, err
	}
	h.d.Logger.Info(fmt.Sprintf("position_memo.wrote symbol=%s ms=%d", pack.PrimarySymbol, time.Since(started).Milliseconds()))
	return MemoCard{ID: apijson.FormatUUID(params.ID), Symbol: pack.PrimarySymbol, Title: draft.Title, Verdict: draft.Verdict}, nil
}
