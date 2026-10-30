package boards

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/users"
	"norviq-api/internal/store/boardsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// The database side of DailyBoardPost.swift: the Daily board's account and
// board (DailyBoardIdentity), and DailyBoardPublisher's insert, skip, reset
// and history. Composing the facts and drafting with the model live in
// daily_*.go; DailyBoardPostJob is DailyPostJob (daily_job.go). The
// daily-board-post-generate command is not ported: v2 has no operator
// command framework (cmd/api serves, migrates and lists routes only).

// DailyDescription is DailyBoardIdentity.description.
const DailyDescription = "One post every day: a Mag 7 stock, a hot stock, inflation, an ETF, or the week in markets. Members can post here too."

// Daily topics (DailyBoardTopic raw values).
const (
	TopicStock     = "stock"
	TopicMag7      = "mag7"
	TopicMacro     = "macro"
	TopicETF       = "etf"
	TopicWeekRecap = "week_recap"
	TopicWeekAhead = "week_ahead"
	TopicIndex     = "index"
	TopicNews      = "news"
)

// DailyCandidate is DailyBoardCandidate. The publisher stores Topic,
// Symbol, Angle and URL; Name, Facts and Grounded feed the model and the
// validator.
type DailyCandidate struct {
	Topic string
	// Name is what the post is about, in words: "NVDA", "SPDR S&P 500 ETF Trust".
	Name   string
	Symbol string
	Facts  []DailyFact
	// Grounded is every number the post may cite, built with the facts.
	Grounded []float64
	Angle    *string
	URL      *string
}

// symbolTag is DailyBoardHistory.tag(symbol): lowercased letters and digits.
func (c DailyCandidate) symbolTag() string {
	var b strings.Builder
	for _, r := range strings.ToLower(c.Symbol) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Tags is DailyBoardCandidate.tags: topic, ticker or angle, and "ai".
func (c DailyCandidate) Tags() []string {
	tags := []string{c.Topic}
	if t := c.symbolTag(); t != "" {
		tags = append(tags, t)
	}
	if c.Angle != nil {
		tags = append(tags, *c.Angle)
	}
	return append(tags, "ai")
}

// DailyOutcome is DailyBoardOutcome.
type DailyOutcome struct {
	// Kind is "alreadyRun", "skipped" or "published".
	Kind   string
	Status string // alreadyRun
	Reason string // skipped
	Topic  string // published
}

func (o DailyOutcome) String() string {
	switch o.Kind {
	case "alreadyRun":
		return "already " + o.Status
	case "skipped":
		return "skipped (" + o.Reason + ")"
	}
	return "published " + o.Topic
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// EnsureDaily is DailyBoardIdentity.ensure: creates the account and the
// Daily board the first time anyone asks. A username already owned by a
// person is left alone.
func (s *Service) EnsureDaily(ctx context.Context) (userID, boardID uuid.UUID, err error) {
	username := users.SystemUsername
	taken := httpx.Conflict("The norviq username is taken.")
	found, err := s.Q.DailyUserByUsername(ctx, &username)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if len(found) == 0 {
		hash, err := users.HashPassword(strings.ToUpper(uuid.NewString()))
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		err = s.Q.DailyInsertSystemUser(ctx, boardsdb.DailyInsertSystemUserParams{
			ID: uuid.New(), Email: users.SystemEmail, PasswordHash: hash, Username: &username, CreatedAt: s.now(),
		})
		// A concurrent request created the same account.
		if err != nil && !isUniqueViolation(err) {
			return uuid.Nil, uuid.Nil, err
		}
		if found, err = s.Q.DailyUserByUsername(ctx, &username); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
	}
	if len(found) == 0 || !strings.EqualFold(found[0].Email, users.SystemEmail) {
		return uuid.Nil, uuid.Nil, taken
	}
	userID = found[0].ID

	boards, err := s.Q.BoardBySlugWithDeleted(ctx, dailySlug)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if len(boards) > 0 {
		b := boards[0]
		if b.DeletedAt != nil {
			if err := s.Q.BoardRestore(ctx, b.ID); err != nil {
				return uuid.Nil, uuid.Nil, err
			}
		}
		// Boards created by an older build keep its wording until updated
		// here. A board someone else made under this slug is left alone.
		if b.CreatorID != nil && *b.CreatorID == userID && b.Description != DailyDescription {
			if err := s.Q.BoardUpdateDescription(ctx, boardsdb.BoardUpdateDescriptionParams{ID: b.ID, Description: DailyDescription}); err != nil {
				return uuid.Nil, uuid.Nil, err
			}
		}
		return userID, b.ID, nil
	}
	now := s.now()
	err = s.Q.BoardInsert(ctx, boardsdb.BoardInsertParams{
		ID: uuid.New(), Slug: dailySlug, Name: "Daily", Description: DailyDescription, CreatorID: &userID,
		LastActivityAt: now, CreatedAt: now,
	})
	// Created by the other replica.
	if err != nil && !isUniqueViolation(err) {
		return uuid.Nil, uuid.Nil, err
	}
	if boards, err = s.Q.BoardBySlugWithDeleted(ctx, dailySlug); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if len(boards) == 0 {
		return uuid.Nil, uuid.Nil, httpx.Abort(500, "Daily board was not created.")
	}
	if boards[0].DeletedAt != nil {
		if err := s.Q.BoardRestore(ctx, boards[0].ID); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
	}
	return userID, boards[0].ID, nil
}

// SkipDaily is DailyBoardPublisher.skip: a skip is final for that date.
func (s *Service) SkipDaily(ctx context.Context, tradingDate string, topic *string) error {
	return writeRun(ctx, s.Q, tradingDate, "skipped", nil, topic, s)
}

// InsertDaily is DailyBoardPublisher.insert: the post and the run row, for
// an already validated draft.
func (s *Service) InsertDaily(ctx context.Context, tradingDate string, c DailyCandidate, title, body string) (DailyOutcome, error) {
	if runs, err := s.Q.DailyRunByDate(ctx, tradingDate); err != nil {
		return DailyOutcome{}, err
	} else if len(runs) > 0 {
		return DailyOutcome{Kind: "alreadyRun", Status: runs[0].Status}, nil
	}
	userID, boardID, err := s.EnsureDaily(ctx)
	if err != nil {
		return DailyOutcome{}, err
	}
	now := s.now()
	kind := KindText
	var link, domain *string
	if c.Topic == TopicNews {
		u, d, err := validateLink(c.URL)
		if err != nil {
			topic := c.Topic
			if err := s.SkipDaily(ctx, tradingDate, &topic); err != nil {
				return DailyOutcome{}, err
			}
			return DailyOutcome{Kind: "skipped", Reason: "headline link rejected"}, nil
		}
		link, domain, kind = &u, &d, KindLink
	}
	topic, date := c.Topic, tradingDate
	post := boardsdb.BoardPost{
		ID: uuid.New(), BoardID: boardID, AuthorID: userID, Kind: kind, Title: title, Url: link, Domain: domain,
		Body: &body, Tags: c.Tags(), LastActivityAt: now, CreatedAt: s.now(), AiGenerated: true, AiTopic: &topic, TradingDate: &date,
	}
	err = s.inTx(ctx, func(q *boardsdb.Queries) error {
		if err := insertPost(ctx, q, &post); err != nil {
			return err
		}
		if err := q.BoardBumpPost(ctx, boardsdb.BoardBumpPostParams{ID: boardID, LastActivityAt: now}); err != nil {
			return err
		}
		return writeRun(ctx, q, tradingDate, "published", &post.ID, &topic, s)
	}, nil)
	if err != nil {
		return DailyOutcome{}, err
	}
	return DailyOutcome{Kind: "published", Topic: c.Topic}, nil
}

// ResetDaily is DailyBoardPublisher.reset: drops the run row and
// soft-deletes that date's AI post, if one is still up.
func (s *Service) ResetDaily(ctx context.Context, tradingDate string) error {
	return s.inTx(ctx, func(q *boardsdb.Queries) error {
		date := tradingDate
		posts, err := q.DailyPostsForDate(ctx, &date)
		if err != nil {
			return err
		}
		now := s.now()
		for i := range posts {
			if err := q.BoardPostSoftDelete(ctx, boardsdb.BoardPostSoftDeleteParams{ID: posts[i].ID, DeletedAt: &now}); err != nil {
				return err
			}
			if err := q.BoardDropPost(ctx, posts[i].BoardID); err != nil {
				return err
			}
		}
		return q.DailyDeleteRun(ctx, tradingDate)
	}, nil)
}

// DailyHistoryEntry is DailyBoardHistory.Entry.
type DailyHistoryEntry struct {
	Day   string
	Topic *string // nil when the stored topic is not a DailyBoardTopic
	Tags  []string
}

// DailyHistory is DailyBoardPublisher.history: the last 60 daily posts
// before day, newest first.
func (s *Service) DailyHistory(ctx context.Context, day string) ([]DailyHistoryEntry, error) {
	rows, err := s.Q.DailyHistory(ctx, &day)
	if err != nil {
		return nil, err
	}
	out := []DailyHistoryEntry{}
	for _, r := range rows {
		if r.TradingDate == nil {
			continue
		}
		e := DailyHistoryEntry{Day: *r.TradingDate, Tags: r.Tags}
		if r.AiTopic != nil && validDailyTopic(*r.AiTopic) {
			t := *r.AiTopic
			e.Topic = &t
		}
		out = append(out, e)
	}
	return out, nil
}

func validDailyTopic(t string) bool {
	switch t {
	case TopicStock, TopicMag7, TopicMacro, TopicETF, TopicWeekRecap, TopicWeekAhead, TopicIndex, TopicNews:
		return true
	}
	return false
}

// writeRun inserts the run row unless the date already has one.
func writeRun(ctx context.Context, q *boardsdb.Queries, tradingDate, status string, postID *uuid.UUID, topic *string, s *Service) error {
	runs, err := q.DailyRunByDate(ctx, tradingDate)
	if err != nil || len(runs) > 0 {
		return err
	}
	now := s.now()
	err = q.DailyInsertRun(ctx, boardsdb.DailyInsertRunParams{
		TradingDate: tradingDate, Status: status, PostID: postID, Topic: topic, CreatedAt: &now,
	})
	if isUniqueViolation(err) {
		return nil
	}
	return err
}
