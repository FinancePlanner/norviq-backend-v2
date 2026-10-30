package groups

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"norviq-api/internal/apijson"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/community"
	"norviq-api/internal/store/groupsdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v5"
)

const messagePageSize = 50

// queryInt64 is `req.query[Int64.self, at:]`: nil when absent or malformed.
func queryInt64(c *echo.Context, name string) *int64 {
	raw := httpx.QueryString(c, name)
	if raw == nil {
		return nil
	}
	n, err := strconv.ParseInt(*raw, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func (h *handler) channelFor(c *echo.Context) (*community.Viewer, *groupsdb.GroupChannel, *groupsdb.Group, error) {
	v, err := community.FromContext(c)
	if err != nil {
		return nil, nil, nil, err
	}
	id, err := uuidParam(c, "channelId")
	if err != nil {
		return nil, nil, nil, err
	}
	ch, g, err := h.s.channel(ctxOf(c), id)
	if err != nil {
		return nil, nil, nil, err
	}
	return v, ch, g, nil
}

// messages is oldest first. With no cursor, the newest page; `before=<seq>`
// pages back, `after=<seq>` catches up. Hidden authors are left out.
func (h *handler) messages(c *echo.Context) error {
	v, ch, g, err := h.channelFor(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	m, err := h.s.requireMember(ctx, g, v)
	if err != nil {
		return err
	}
	limit := messagePageSize
	if l := httpx.QueryOptionalInt(c, "limit"); l != nil {
		limit = *l
	}
	limit = min(max(limit, 1), 100)
	before, after := queryInt64(c, "before"), queryInt64(c, "after")
	if before != nil && after != nil {
		return httpx.BadRequest("Use either before or after, not both.")
	}
	hidden, err := h.s.hidden(ctx, v.UserID)
	if err != nil {
		return err
	}
	q := h.s.q()
	var rows []groupsdb.GroupMessage
	var hasMoreBefore, hasMoreAfter bool
	if after != nil {
		rows, err = q.MessagesAfter(ctx, groupsdb.MessagesAfterParams{ChannelID: ch.ID, Hidden: keys(hidden), After: *after, Lim: int64(limit + 1)})
		if err != nil {
			return err
		}
		hasMoreAfter = len(rows) > limit
		rows = rows[:min(len(rows), limit)]
		first := *after + 1
		if len(rows) > 0 {
			first = rows[0].Seq
		}
		hasMoreBefore = first > 1
	} else {
		rows, err = q.MessagesBefore(ctx, groupsdb.MessagesBeforeParams{ChannelID: ch.ID, Hidden: keys(hidden), Before: before, Lim: int64(limit + 1)})
		if err != nil {
			return err
		}
		hasMoreBefore = len(rows) > limit
		rows = rows[:min(len(rows), limit)]
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
		last := int64(0)
		if before != nil {
			last = *before - 1
		}
		if len(rows) > 0 {
			last = rows[len(rows)-1].Seq
		}
		hasMoreAfter = last < ch.LastSeq
	}
	reads, err := q.ReadFor(ctx, groupsdb.ReadForParams{ChannelID: ch.ID, UserID: m.UserID})
	if err != nil {
		return err
	}
	authors := make([]uuid.UUID, len(rows))
	for i := range rows {
		authors[i] = rows[i].AuthorID
	}
	names, err := h.s.usernames(ctx, authors)
	if err != nil {
		return err
	}
	out := GroupMessagePage{Items: make([]GroupMessage, 0, len(rows)), HasMoreBefore: hasMoreBefore, HasMoreAfter: hasMoreAfter}
	for i := range rows {
		out.Items = append(out.Items, messageDTO(&rows[i], names))
	}
	if len(reads) > 0 {
		out.LastReadSeq = reads[0].LastReadSeq
	}
	return httpx.OK(c, out)
}

var sendFields = []field{{name: "clientMessageId", kind: fString}, {name: "body", kind: fString}}

// send answers 201 with the new message, or 200 with the original when this
// clientMessageId was already sent.
func (h *handler) send(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	if err := v.RequireCanContribute(); err != nil {
		return err
	}
	_, ch, g, err := h.channelFor(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	if _, err := h.s.requireMember(ctx, g, v); err != nil {
		return err
	}
	body, err := decodeBody(c, sendFields)
	if err != nil {
		return err
	}
	clientID, err := validateClientMessageID(*body.str("clientMessageId"))
	if err != nil {
		return err
	}
	names := map[uuid.UUID]string{}
	if v.Username != nil {
		names[v.UserID] = *v.Username
	}
	if existing, err := h.existingMessage(ctx, v.UserID, clientID); err != nil {
		return err
	} else if existing != nil {
		return httpx.OK(c, messageDTO(existing, names))
	}
	text, err := validateMessageBody(*body.str("body"))
	if err != nil {
		return err
	}
	now := h.s.now()
	today, err := h.s.q().MessageCountByAuthorSince(ctx, groupsdb.MessageCountByAuthorSinceParams{AuthorID: v.UserID, CreatedAt: now.Add(-86400e9)})
	if err != nil {
		return err
	}
	if today >= maxMessagesPerDay {
		return httpx.CodedAbort(http.StatusTooManyRequests, "message_daily_limit",
			"You've reached today's message limit. Try again tomorrow.")
	}
	msg := groupsdb.GroupMessage{ID: uuid.New(), ChannelID: ch.ID, GroupID: g.ID, AuthorID: v.UserID, ClientMessageID: clientID, Body: text, CreatedAt: now}
	err = h.s.tx(ctx, func(tq *groupsdb.Queries) error {
		seqs, err := tq.ChannelBumpSeq(ctx, groupsdb.ChannelBumpSeqParams{ID: ch.ID, LastMessageAt: &now})
		if err != nil {
			return err
		}
		if len(seqs) == 0 {
			return errChannelNotFound()
		}
		msg.Seq = seqs[0]
		if err := tq.MessageInsert(ctx, groupsdb.MessageInsertParams{
			ID: msg.ID, ChannelID: msg.ChannelID, GroupID: msg.GroupID, AuthorID: msg.AuthorID, Seq: msg.Seq,
			ClientMessageID: msg.ClientMessageID, Body: msg.Body, CreatedAt: msg.CreatedAt,
		}); err != nil {
			return err
		}
		if err := tq.GroupSetLastMessageAt(ctx, groupsdb.GroupSetLastMessageAtParams{ID: g.ID, LastMessageAt: &now}); err != nil {
			return err
		}
		// Your own message is never unread to you.
		return tq.ReadUpsert(ctx, groupsdb.ReadUpsertParams{ID: uuid.New(), ChannelID: ch.ID, UserID: v.UserID, LastReadSeq: msg.Seq, UpdatedAt: now})
	})
	if err != nil {
		// Two copies of the same send raced; the other one won.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code[:2] == "23" {
			if existing, e := h.existingMessage(ctx, v.UserID, clientID); e == nil && existing != nil {
				return httpx.OK(c, messageDTO(existing, names))
			}
		}
		return err
	}
	dto := messageDTO(&msg, names)
	author := v.UserID
	h.pub.toMembersOf(ctx, GroupRealtimeEvent{
		Type: EventMessageCreated, GroupID: uuidPtr(g.ID), ChannelID: uuidPtr(ch.ID), Message: &dto,
	}, g.ID, &author)
	h.n.messageCreated(dto, g.Name, ch.Name, v.Username)
	return httpx.Created(c, dto)
}

var markReadFields = []field{{name: "seq", kind: fInt64}}

// markRead moves the viewer's read marker forward; a lower seq is ignored.
func (h *handler) markRead(c *echo.Context) error {
	v, ch, g, err := h.channelFor(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	if _, err := h.s.requireMember(ctx, g, v); err != nil {
		return err
	}
	body, err := decodeBody(c, markReadFields)
	if err != nil {
		return err
	}
	seq := min(max(body.int64v("seq"), 0), ch.LastSeq)
	if err := h.s.q().ReadUpsert(ctx, groupsdb.ReadUpsertParams{
		ID: uuid.New(), ChannelID: ch.ID, UserID: v.UserID, LastReadSeq: seq, UpdatedAt: h.s.now(),
	}); err != nil {
		return err
	}
	// The viewer's other devices clear their badge.
	h.pub.to(GroupRealtimeEvent{Type: EventChannelRead, GroupID: uuidPtr(g.ID), ChannelID: uuidPtr(ch.ID), Seq: &seq}, one(v.UserID), nil)
	return noContent(c)
}

// deleteMessage: the author, the group's owner or admins, or a global admin.
// Leaves a tombstone; the body is kept for moderators.
func (h *handler) deleteMessage(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	msg, g, err := h.messageFor(c)
	if err != nil {
		return err
	}
	if msg.AuthorID != v.UserID && !v.IsAdmin {
		m, err := h.s.requireMember(ctx, g, v)
		if err != nil {
			return err
		}
		if err := requireManager(m); err != nil {
			return err
		}
	}
	if msg.DeletedAt == nil {
		now := h.s.now()
		by := v.UserID
		if err := h.s.q().MessageSoftDelete(ctx, groupsdb.MessageSoftDeleteParams{ID: msg.ID, DeletedAt: &now, DeletedBy: &by}); err != nil {
			return err
		}
		seq := msg.Seq
		h.pub.toMembersOf(ctx, GroupRealtimeEvent{
			Type: EventMessageDeleted, GroupID: uuidPtr(msg.GroupID), ChannelID: uuidPtr(msg.ChannelID),
			MessageID: uuidPtr(msg.ID), Seq: &seq,
		}, msg.GroupID, nil)
	}
	return noContent(c)
}

func (h *handler) messageFor(c *echo.Context) (*groupsdb.GroupMessage, *groupsdb.Group, error) {
	id, err := uuidParam(c, "messageId")
	if err != nil {
		return nil, nil, err
	}
	ctx := ctxOf(c)
	q := h.s.q()
	rows, err := q.MessageByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, httpx.NotFound("Message not found")
	}
	g, err := h.s.group(ctx, q, rows[0].GroupID)
	if err != nil {
		return nil, nil, err
	}
	return &rows[0], g, nil
}

var reportFields = []field{
	{name: "reason", kind: fEnum, enumName: "BoardReportReason", valid: validReportReason},
	{name: "note", kind: fString, optional: true},
}

// report lands in the same social_reports queue as board reports and pings
// Discord. It skips the contribution gate.
func (h *handler) report(c *echo.Context) error {
	v, err := community.FromContext(c)
	if err != nil {
		return err
	}
	ctx := ctxOf(c)
	msg, g, err := h.messageFor(c)
	if err != nil {
		return err
	}
	if _, err := h.s.requireMember(ctx, g, v); err != nil {
		return err
	}
	body, err := decodeBody(c, reportFields)
	if err != nil {
		return err
	}
	var note *string
	if n := body.str("note"); n != nil {
		t := trim(*n)
		if charCount(t) > 1000 {
			return httpx.BadRequest("Keep the details under 1,000 characters.")
		}
		if t != "" {
			note = &t
		}
	}
	reason := *body.str("reason")
	if err := h.s.q().GroupReportInsert(ctx, groupsdb.GroupReportInsertParams{
		ID: uuid.New(), ReporterID: v.UserID, TargetType: "group_message", TargetID: apijson.FormatUUID(msg.ID),
		Reason: reason, Note: note, CreatedAt: h.s.now(),
	}); err != nil {
		return err
	}
	h.log.Info("groups.report filed", "reason", reason)
	text := "🚩 Group report (" + reason + ") in " + g.Name + ":\n```" + prefixChars(msg.Body, 300) + "```"
	go func() {
		if err := h.discord.Send(context.Background(), text); err != nil {
			h.log.Debug("Failed to send discord group report notification")
		}
	}()
	return c.NoContent(http.StatusAccepted)
}

func (h *handler) existingMessage(ctx context.Context, author uuid.UUID, clientID string) (*groupsdb.GroupMessage, error) {
	rows, err := h.s.q().MessageByClientID(ctx, groupsdb.MessageByClientIDParams{AuthorID: author, ClientMessageID: clientID})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}
