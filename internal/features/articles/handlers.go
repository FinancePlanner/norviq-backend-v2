package articles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/auth"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/achievementhooks"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/platform/discord"
	"norviq-api/internal/server"
	"norviq-api/internal/store/articlesdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v5"
)

const (
	pageSize    = 20
	maxPageSize = 100
	// viewerHeader carries the web's hashed visitor key.
	viewerHeader = "X-Norviq-Viewer"
	// uploadMaxBody is `body: .collect(maxSize: "3mb")`; Vapor refuses a body
	// that reaches it.
	uploadMaxBody = 3 << 20
)

type handler struct {
	d           *server.Deps
	q           *articlesdb.Queries
	access      *community.Access
	verifier    TickerVerifier
	discord     discord.Sender
	now         func() time.Time
	viewerCreds map[uuid.UUID]struct{}
}

func notFoundArticle() error { return httpx.NotFound("Article not found") }

// --- wire types (StockPlanShared ArticlesDTOs) ------------------------------

type articleAuthor struct {
	ID        apijson.UUID `json:"id"`
	Username  *string      `json:"username,omitempty"`
	AvatarURL *string      `json:"avatarURL,omitempty"`
}

type articleSummary struct {
	ID           apijson.UUID  `json:"id"`
	Code         string        `json:"code"`
	Slug         string        `json:"slug"`
	Title        string        `json:"title"`
	BulletPoints []string      `json:"bulletPoints"`
	Tickers      []string      `json:"tickers"`
	Author       articleAuthor `json:"author"`
	CoverImageID *apijson.UUID `json:"coverImageId,omitempty"`
	UpvoteCount  int64         `json:"upvoteCount"`
	ViewCount    int64         `json:"viewCount"`
	WordCount    int64         `json:"wordCount"`
	Status       string        `json:"status"`
	Source       string        `json:"source"`
	PublishedAt  apijson.Time  `json:"publishedAt"`
	EditedAt     *apijson.Time `json:"editedAt,omitempty"`
}

type articleDetail struct {
	Article        articleSummary `json:"article"`
	BodyMarkdown   string         `json:"bodyMarkdown"`
	Disclosure     string         `json:"disclosure"`
	ViewerUpvoted  bool           `json:"viewerUpvoted"`
	ViewerIsAuthor bool           `json:"viewerIsAuthor"`
}

type listResponse struct {
	Items      []articleSummary `json:"items"`
	NextCursor *string          `json:"nextCursor,omitempty"`
}

type voteResponse struct {
	UpvoteCount int64 `json:"upvoteCount"`
	Voted       bool  `json:"voted"`
}

type imageUploadResponse struct {
	ID apijson.UUID `json:"id"`
}

type authorRow struct {
	Username  *string
	AvatarURL *string
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// summary is ArticlePresenter.summary. Unknown status/source raw values
// present as "unknown".
func summary(a articlesdb.Article, author *authorRow) articleSummary {
	s := articleSummary{
		ID: apijson.NewUUID(a.ID), Code: a.Code, Slug: a.Slug, Title: a.Title,
		BulletPoints: nonNil(a.BulletPoints), Tickers: nonNil(a.Tickers),
		Author:      articleAuthor{ID: apijson.NewUUID(a.AuthorID)},
		UpvoteCount: a.UpvoteCount, ViewCount: a.ViewCount, WordCount: a.WordCount,
		Status: "unknown", Source: "unknown",
		PublishedAt: apijson.NewTime(a.PublishedAt), EditedAt: apijson.TimePtr(a.EditedAt),
	}
	if author != nil {
		s.Author.Username, s.Author.AvatarURL = author.Username, author.AvatarURL
	}
	if a.CoverImageID != nil {
		id := apijson.NewUUID(*a.CoverImageID)
		s.CoverImageID = &id
	}
	switch a.Status {
	case "published", "hidden", "deleted":
		s.Status = a.Status
	}
	switch a.Source {
	case "web", "ios", "discord":
		s.Source = a.Source
	}
	return s
}

// summaries is ArticlePresenter.summaries: one users query for the page.
func (h *handler) summaries(ctx context.Context, page []articlesdb.Article) ([]articleSummary, error) {
	out := make([]articleSummary, 0, len(page))
	if len(page) == 0 {
		return out, nil
	}
	seen := map[uuid.UUID]bool{}
	ids := []uuid.UUID{}
	for _, a := range page {
		if !seen[a.AuthorID] {
			seen[a.AuthorID] = true
			ids = append(ids, a.AuthorID)
		}
	}
	rows, err := h.q.ArticleAuthors(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]*authorRow{}
	for _, r := range rows {
		byID[r.ID] = &authorRow{Username: r.Username, AvatarURL: r.AvatarUrl}
	}
	for _, a := range page {
		out = append(out, summary(a, byID[a.AuthorID]))
	}
	return out, nil
}

// detail is ArticlePresenter.detail.
func (h *handler) detail(ctx context.Context, a articlesdb.Article, viewer *uuid.UUID) (articleDetail, error) {
	rows, err := h.q.ArticleAuthor(ctx, a.AuthorID)
	if err != nil {
		return articleDetail{}, err
	}
	var author *authorRow
	if len(rows) > 0 {
		author = &authorRow{Username: rows[0].Username, AvatarURL: rows[0].AvatarUrl}
	}
	upvoted := false
	if viewer != nil {
		if upvoted, err = h.q.ArticleVoteExists(ctx, articlesdb.ArticleVoteExistsParams{ArticleID: a.ID, UserID: *viewer}); err != nil {
			return articleDetail{}, err
		}
	}
	return articleDetail{
		Article: summary(a, author), BodyMarkdown: a.BodyMarkdown, Disclosure: a.Disclosure,
		ViewerUpvoted: upvoted, ViewerIsAuthor: viewer != nil && *viewer == a.AuthorID,
	}, nil
}

// --- ArticleService ----------------------------------------------------------

// viewerID is ArticleService.viewerId: the signed-in person behind a
// first-party session. A third-party token (the web's public token, a PAT)
// is nobody: it reads anonymously.
func viewerID(c *echo.Context) *uuid.UUID {
	s := auth.FromContext(c)
	if s == nil || s.Scope != nil {
		return nil
	}
	id := s.UserID
	return &id
}

// isListed is ArticleViewerCredentials.isListed.
func (h *handler) isListed(c *echo.Context) bool {
	s := auth.FromContext(c)
	if s == nil || s.Scope == nil {
		return false
	}
	_, ok := h.viewerCreds[s.Scope.TokenID]
	return ok
}

// isAdmin is ArticleService.isAdmin: the community viewer's admin flag, false
// when the viewer cannot be resolved.
func (h *handler) isAdmin(ctx context.Context, id uuid.UUID) bool {
	v, err := h.access.Viewer(ctx, id)
	return err == nil && v.IsAdmin
}

// find is ArticleService.find: `ref` is the article's uuid or its
// 8-character code.
func (h *handler) find(ctx context.Context, ref string) (*articlesdb.Article, error) {
	if id, ok := parseSwiftUUID(ref); ok {
		a, err := h.q.ArticleByID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &a, nil
	}
	code := swiftLower(ref)
	if graphemeLen(code) != 8 {
		return nil, nil
	}
	rows, err := h.q.ArticleByCode(ctx, code)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// requireVisible is ArticleService.requireVisible: published articles are
// visible to all, hidden ones to their author and admins, deleted ones to
// admins; everyone else gets the same 404 as a missing article.
func (h *handler) requireVisible(ctx context.Context, ref string, viewer *uuid.UUID) (articlesdb.Article, error) {
	a, err := h.find(ctx, ref)
	if err != nil {
		return articlesdb.Article{}, err
	}
	if a == nil {
		return articlesdb.Article{}, notFoundArticle()
	}
	switch a.Status {
	case "published":
		return *a, nil
	case "hidden":
		if viewer != nil && *viewer == a.AuthorID {
			return *a, nil
		}
		if viewer != nil && h.isAdmin(ctx, *viewer) {
			return *a, nil
		}
	default:
		if viewer != nil && h.isAdmin(ctx, *viewer) {
			return *a, nil
		}
	}
	return articlesdb.Article{}, notFoundArticle()
}

type fields struct {
	Title, Slug, Body, Disclosure string
	BulletPoints, Tickers         []string
	CoverImageID                  *uuid.UUID
	WordCount                     int
}

// validate is ArticleService.validate.
func (h *handler) validate(ctx context.Context, in writeRequest, authorID uuid.UUID) (fields, error) {
	var f fields
	var err error
	if f.Title, err = validateTitle(in.Title); err != nil {
		return f, err
	}
	if f.Body, err = validateBody(in.BodyMarkdown); err != nil {
		return f, err
	}
	if f.BulletPoints, err = validateBullets(in.BulletPoints); err != nil {
		return f, err
	}
	if f.Tickers, err = validateTickers(in.Tickers); err != nil {
		return f, err
	}
	if f.Disclosure, err = validateDisclosure(in.Disclosure); err != nil {
		return f, err
	}
	for _, t := range f.Tickers {
		if h.verifier.Check(ctx, t) == TickerMissing {
			return f, httpx.BadRequest("We couldn't find the ticker " + t + ".")
		}
	}
	if in.CoverImageID != nil {
		owners, err := h.q.ArticleImageOwner(ctx, *in.CoverImageID)
		if err != nil {
			return f, err
		}
		if len(owners) == 0 || owners[0] != authorID {
			return f, httpx.BadRequest("Upload the cover image again.")
		}
	}
	f.Slug = slugFrom(f.Title)
	f.CoverImageID = in.CoverImageID
	f.WordCount = wordCount(f.Body)
	return f, nil
}

// --- reads -------------------------------------------------------------------

func (h *handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	limit := min(max(httpx.IntOr(httpx.QueryOptionalInt(c, "limit"), pageSize), 1), maxPageSize)
	p := articlesdb.ListPublishedArticlesParams{RowLimit: int32(limit + 1)}
	if raw := httpx.QueryString(c, "ticker"); raw != nil && *raw != "" {
		tickers, err := validateTickers([]string{*raw})
		if err != nil {
			return err
		}
		p.Ticker = &tickers[0]
	}
	if username := httpx.QueryString(c, "author"); username != nil && *username != "" {
		ids, err := h.q.UserIDByUsername(ctx, username)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return httpx.OK(c, listResponse{Items: []articleSummary{}})
		}
		p.AuthorID = &ids[0]
	}
	if raw := httpx.QueryString(c, "cursor"); raw != nil {
		if at, id, ok := decodeCursor(*raw); ok {
			p.CursorAt, p.CursorID = &at, &id
		}
	}
	rows, err := h.q.ListPublishedArticles(ctx, p)
	if err != nil {
		return err
	}
	page := rows
	if len(page) > limit {
		page = page[:limit]
	}
	items, err := h.summaries(ctx, page)
	if err != nil {
		return err
	}
	res := listResponse{Items: items}
	if len(rows) > limit {
		next := encodeCursor(page[len(page)-1])
		res.NextCursor = &next
	}
	return httpx.OK(c, res)
}

// encodeCursor is `<epoch milliseconds>_<uuid>`: stable across equal
// timestamps.
func encodeCursor(a articlesdb.Article) string {
	return strconv.FormatInt(cursorMillis(a.PublishedAt), 10) + "_" + apijson.FormatUUID(a.ID)
}

// decodeCursor is decodeCursor: `split(separator: "_", maxSplits: 1)`, which
// skips leading separators; a malformed cursor is ignored.
func decodeCursor(raw string) (time.Time, uuid.UUID, bool) {
	ms, rest, ok := strings.Cut(strings.TrimLeft(raw, "_"), "_")
	if !ok || ms == "" || rest == "" {
		return time.Time{}, uuid.UUID{}, false
	}
	millis, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return time.Time{}, uuid.UUID{}, false
	}
	id, ok := parseSwiftUUID(rest)
	if !ok {
		return time.Time{}, uuid.UUID{}, false
	}
	return pgBind(float64(millis) / 1000), id, true
}

func (h *handler) get(c *echo.Context) error {
	ctx := c.Request().Context()
	viewer := viewerID(c)
	a, err := h.requireVisible(ctx, c.Param("ref"), viewer)
	if err != nil {
		return err
	}
	d, err := h.detail(ctx, a, viewer)
	if err != nil {
		return err
	}
	return httpx.OK(c, d)
}

// image serves a cover while a published article uses it. Otherwise only to
// its owner's or an admin's own session (a third-party credential is
// nobody); everyone else gets the same 404 as a missing image.
func (h *handler) image(c *echo.Context) error {
	ctx := c.Request().Context()
	id, ok := parseSwiftUUID(c.Param("imageId"))
	if !ok {
		return httpx.NotFound("")
	}
	img, err := h.q.ArticleImageByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("")
	}
	if err != nil {
		return err
	}
	covers, err := h.q.CountPublishedCovers(ctx, &id)
	if err != nil {
		return err
	}
	isPublishedCover := covers > 0
	if !isPublishedCover {
		viewer := viewerID(c)
		if viewer == nil {
			return httpx.NotFound("")
		}
		if *viewer != img.OwnerID && !h.isAdmin(ctx, *viewer) {
			return httpx.NotFound("")
		}
	}
	hdr := c.Response().Header()
	// A day, not forever: a takedown has to reach caches. An image only its
	// owner or an admin may see must not land in a shared cache at all.
	if isPublishedCover {
		hdr.Set("Cache-Control", "public, max-age=86400")
	} else {
		hdr.Set("Cache-Control", "private, no-store")
	}
	hdr.Set("X-Content-Type-Options", "nosniff")
	return c.Blob(http.StatusOK, img.ContentType, img.Bytes)
}

// --- writes ------------------------------------------------------------------

func (h *handler) create(c *echo.Context) error {
	ctx := c.Request().Context()
	viewer, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := viewer.RequireCanContribute(); err != nil {
		return err
	}
	in, err := decodeWriteRequest(c)
	if err != nil {
		return err
	}
	now := h.now()
	// Before validation, which spends market-data quota on ticker lookups.
	if !viewer.IsAdmin {
		recent, err := h.q.CountArticlesSince(ctx, articlesdb.CountArticlesSinceParams{
			AuthorID: viewer.UserID, PublishedAt: now.Add(-86400 * time.Second),
		})
		if err != nil {
			return err
		}
		if recent >= maxPerDay {
			return tooMany("article_daily_limit", "You can publish 3 articles a day.")
		}
	}
	f, err := h.validate(ctx, in, viewer.UserID)
	if err != nil {
		return err
	}
	source := "web"
	if in.Source != nil && *in.Source != "unknown" {
		source = *in.Source
	}
	// Whole milliseconds, so the feed cursor (epoch ms) matches exactly.
	published := wholeMillis(now)
	a := articlesdb.Article{
		AuthorID: viewer.UserID, Slug: f.Slug, Title: f.Title, BodyMarkdown: f.Body,
		BulletPoints: f.BulletPoints, Tickers: f.Tickers, Disclosure: f.Disclosure,
		CoverImageID: f.CoverImageID, Status: "published", Source: source,
		WordCount: int64(f.WordCount), PublishedAt: fromSeconds(published), CreatedAt: now,
	}
	if err := h.insert(ctx, &a, pgBind(published)); err != nil {
		return err
	}
	h.d.Logger.Info("articles.published", "code", a.Code, "source", source)
	d, err := h.detail(ctx, a, &viewer.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, d)
}

// insert is ArticleService.insert: a fresh code, retried on the rare
// constraint failure (the fifth failure is returned).
func (h *handler) insert(ctx context.Context, a *articlesdb.Article, publishedBind time.Time) error {
	for attempt := 1; ; attempt++ {
		a.ID = uuid.New()
		a.Code = makeCode()
		err := h.q.InsertArticle(ctx, articlesdb.InsertArticleParams{
			ID: a.ID, Code: a.Code, AuthorID: a.AuthorID, Slug: a.Slug, Title: a.Title,
			BodyMarkdown: a.BodyMarkdown, BulletPoints: a.BulletPoints, Tickers: a.Tickers,
			Disclosure: a.Disclosure, CoverImageID: a.CoverImageID, Status: a.Status, Source: a.Source,
			WordCount: a.WordCount, PublishedAt: publishedBind, CreatedAt: a.CreatedAt,
		})
		var pgErr *pgconn.PgError
		if err != nil && attempt < 5 && errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, "23") {
			continue
		}
		return err
	}
}

func (h *handler) update(c *echo.Context) error {
	ctx := c.Request().Context()
	viewer, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := viewer.RequireCanContribute(); err != nil {
		return err
	}
	a, err := h.requireVisible(ctx, c.Param("ref"), &viewer.UserID)
	if err != nil {
		return err
	}
	if a.AuthorID != viewer.UserID {
		return httpx.Forbidden("You can only edit your own articles.")
	}
	in, err := decodeWriteRequest(c)
	if err != nil {
		return err
	}
	f, err := h.validate(ctx, in, viewer.UserID)
	if err != nil {
		return err
	}
	now := h.now()
	if err := h.q.UpdateArticleContent(ctx, articlesdb.UpdateArticleContentParams{
		Title: f.Title, Slug: f.Slug, BodyMarkdown: f.Body, BulletPoints: f.BulletPoints, Tickers: f.Tickers,
		Disclosure: f.Disclosure, CoverImageID: f.CoverImageID, WordCount: int64(f.WordCount),
		EditedAt: &now, UpdatedAt: &now, ID: a.ID,
	}); err != nil {
		return err
	}
	a.Title, a.Slug, a.BodyMarkdown, a.BulletPoints, a.Tickers = f.Title, f.Slug, f.Body, f.BulletPoints, f.Tickers
	a.Disclosure, a.CoverImageID, a.WordCount, a.EditedAt = f.Disclosure, f.CoverImageID, int64(f.WordCount), &now
	d, err := h.detail(ctx, a, &viewer.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(c, d)
}

func (h *handler) delete(c *echo.Context) error {
	ctx := c.Request().Context()
	viewer, err := community.FromContext(c)
	if err != nil {
		return err
	}
	a, err := h.requireVisible(ctx, c.Param("ref"), &viewer.UserID)
	if err != nil {
		return err
	}
	if a.AuthorID != viewer.UserID && !viewer.IsAdmin {
		return httpx.Forbidden("You can only delete your own articles.")
	}
	// Fluent's save writes nothing (not even updated_at) when nothing changed.
	if a.Status != "deleted" {
		now := h.now()
		if err := h.q.SetArticleStatus(ctx, articlesdb.SetArticleStatusParams{Status: "deleted", UpdatedAt: &now, ID: a.ID}); err != nil {
			return err
		}
	}
	return httpx.NoContent(c)
}

// --- engagement --------------------------------------------------------------

// isSwiftHexDigit is Character.isHexDigit: ASCII and fullwidth hex digits.
func isSwiftHexDigit(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' ||
		r >= 0xFF10 && r <= 0xFF19 || r >= 0xFF21 && r <= 0xFF26 || r >= 0xFF41 && r <= 0xFF46
}

// utcDay is `yyyy-MM-dd` in UTC.
func utcDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

// view counts at most one view per viewer per UTC day. A first-party session
// is keyed by user; the web's listed credential forwards a hashed visitor
// key. Any other caller (another PAT, OAuth) counts nothing.
func (h *handler) view(c *echo.Context) error {
	ctx := c.Request().Context()
	a, err := h.requireVisible(ctx, c.Param("ref"), nil)
	if err != nil {
		return err
	}
	var key string
	user := viewerID(c)
	header := c.Request().Header.Values(viewerHeader)
	switch {
	case user != nil:
		key = "u:" + apijson.FormatUUID(*user)
	case h.isListed(c) && len(header) > 0 && validViewerKey(header[0]):
		key = "v:" + swiftLower(header[0])
	default:
		return httpx.NoContent(c)
	}
	inserted, err := h.q.InsertArticleView(ctx, articlesdb.InsertArticleViewParams{
		ID: uuid.New(), ArticleID: a.ID, ViewerKey: key, Day: utcDay(h.now()),
	})
	if err != nil {
		return err
	}
	if len(inserted) > 0 {
		if err := h.q.IncrementArticleViews(ctx, a.ID); err != nil {
			return err
		}
		// Only signed-in readers earn; hashed web visitors have no account.
		if user != nil {
			achievementhooks.Schedule(*user)
		}
	}
	return httpx.NoContent(c)
}

func validViewerKey(v string) bool {
	if n := graphemeLen(v); n < 16 || n > 64 {
		return false
	}
	for _, r := range v {
		if !isSwiftHexDigit(r) {
			return false
		}
	}
	return true
}

func (h *handler) vote(c *echo.Context) error   { return h.setVote(c, true) }
func (h *handler) unvote(c *echo.Context) error { return h.setVote(c, false) }

// setVote moves the vote row and the counter in one transaction, so the
// counter is always the row count.
func (h *handler) setVote(c *echo.Context, on bool) error {
	ctx := c.Request().Context()
	viewer, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := viewer.RequireCanContribute(); err != nil {
		return err
	}
	a, err := h.requireVisible(ctx, c.Param("ref"), nil)
	if err != nil {
		return err
	}
	tx, err := h.d.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := h.q.WithTx(tx)
	var changed []uuid.UUID
	if on {
		changed, err = q.InsertArticleVote(ctx, articlesdb.InsertArticleVoteParams{
			ID: uuid.New(), ArticleID: a.ID, UserID: viewer.UserID, CreatedAt: h.now(),
		})
	} else {
		changed, err = q.DeleteArticleVote(ctx, articlesdb.DeleteArticleVoteParams{ArticleID: a.ID, UserID: viewer.UserID})
	}
	if err != nil {
		return err
	}
	var delta int64
	if len(changed) > 0 {
		delta = -1
		if on {
			delta = 1
		}
	}
	counts, err := q.BumpArticleUpvotes(ctx, articlesdb.BumpArticleUpvotesParams{Delta: delta, ID: a.ID})
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	count := a.UpvoteCount
	if len(counts) > 0 {
		count = counts[0]
	}
	return httpx.OK(c, voteResponse{UpvoteCount: count, Voted: on})
}

// report skips RequireCanContribute on purpose: a muted person must still be
// able to flag a scam.
func (h *handler) report(c *echo.Context) error {
	ctx := c.Request().Context()
	viewer, err := community.FromContext(c)
	if err != nil {
		return err
	}
	a, err := h.requireVisible(ctx, c.Param("ref"), nil)
	if err != nil {
		return err
	}
	body, err := decodeReportRequest(c)
	if err != nil {
		return err
	}
	var note *string
	if body.Note != nil {
		n := trimWS(*body.Note)
		if graphemeLen(n) > 1000 {
			return httpx.BadRequest("Keep the details under 1,000 characters.")
		}
		if n != "" {
			note = &n
		}
	}
	if err := h.q.InsertArticleReport(ctx, articlesdb.InsertArticleReportParams{
		ID: uuid.New(), ReporterID: viewer.UserID, TargetID: apijson.FormatUUID(a.ID),
		Reason: body.Reason, Note: note, CreatedAt: h.now(),
	}); err != nil {
		return err
	}
	h.d.Logger.Info("articles.report filed", "code", a.Code, "reason", body.Reason)

	excerpt := a.Code + " · " + a.Title
	text := fmt.Sprintf("🚩 Article report (%s):\n```%s```", body.Reason, graphemePrefix(excerpt, 300))
	go func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := h.discord.Send(ctx, text); err != nil {
			h.d.Logger.Warn("articles.report discord ping failed", "error", err)
		}
	}(context.WithoutCancel(ctx))
	return httpx.NoContent(c)
}

// --- images ------------------------------------------------------------------

// uploadImage caps uploads per day in the database, and each one sweeps the
// owner's day-old images that no article uses, so abandoned drafts can't pile
// up bytes.
func (h *handler) uploadImage(c *echo.Context) error {
	ctx := c.Request().Context()
	// Vapor collects the body (up to 3mb) before the handler runs.
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, uploadMaxBody))
	if err != nil {
		return err
	}
	if len(body) >= uploadMaxBody {
		return httpx.Abort(http.StatusRequestEntityTooLarge, "")
	}
	viewer, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := viewer.RequireCanContribute(); err != nil {
		return err
	}
	now := h.now()
	dayAgo := now.Add(-86400 * time.Second)
	if !viewer.IsAdmin {
		recent, err := h.q.CountImagesSince(ctx, articlesdb.CountImagesSinceParams{OwnerID: viewer.UserID, CreatedAt: dayAgo})
		if err != nil {
			return err
		}
		if recent >= maxImagesPerDay {
			return tooMany("article_image_daily_limit", "You can upload 10 images a day.")
		}
	}
	data, err := decodeImageUpload(c, body)
	if err != nil {
		return err
	}
	img, err := sniffImage(data)
	if err != nil {
		return err
	}
	id := uuid.New()
	if err := h.q.InsertArticleImage(ctx, articlesdb.InsertArticleImageParams{
		ID: id, OwnerID: viewer.UserID, ContentType: img.ContentType, Bytes: data,
		Width: int64(img.Width), Height: int64(img.Height), CreatedAt: now,
	}); err != nil {
		return err
	}
	if err := h.q.SweepOrphanImages(ctx, articlesdb.SweepOrphanImagesParams{OwnerID: viewer.UserID, CreatedAt: dayAgo}); err != nil {
		h.d.Logger.Warn("articles.image_sweep failed", "error", err)
	}
	return httpx.OK(c, imageUploadResponse{ID: apijson.NewUUID(id)})
}

