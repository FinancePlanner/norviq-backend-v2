package digest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"norviq-api/internal/apijson"
	"norviq-api/internal/platform/mailer"
	"norviq-api/internal/store/digestdb"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Runner is DigestRunner: claims due digests, composes and delivers them.
//
// At most once, per digest and per channel: the weekly_digests row is
// inserted before anything is read (unique on user and week), and each
// channel's weekly_digest_deliveries row before anything is sent (unique on
// digest and channel). A crash or a second process costs a missed message,
// never a duplicate one.
type Runner struct {
	Config  Config
	DB      *pgxpool.Pool
	Sources Sources
	Mailer  mailer.Mailer
	Log     *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// RunSummary is DigestRunner.Summary.
type RunSummary struct {
	Claimed, Sent, Failed int
}

// OutcomeKind is DigestRunner.Outcome's case.
type OutcomeKind int

const (
	// AlreadyClaimed: this period was claimed by this or another run.
	AlreadyClaimed OutcomeKind = iota
	Sent
	Failed
)

// Outcome is DigestRunner.Outcome.
type Outcome struct {
	Kind     OutcomeKind
	DigestID uuid.UUID
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) q() *digestdb.Queries { return digestdb.New(r.DB) }

// RunDue is one scheduler pass: every user whose send time has come, up to
// MaxPerTick.
func (r *Runner) RunDue(ctx context.Context, now time.Time) (RunSummary, error) {
	var s RunSummary
	prefs, err := r.q().DigestPreferencesEmailEnabled(ctx)
	if err != nil {
		return s, err
	}
	for i := range prefs {
		if s.Claimed >= r.Config.MaxPerTick {
			break
		}
		pref := &prefs[i]
		if !r.Config.Allows(pref.UserID) || !r.hasActiveChannel(pref) {
			continue
		}
		loc, ok := loadTimeZone(pref.Timezone)
		if !ok {
			continue
		}
		period, due := DuePeriod(now, loc, int(pref.Weekday), int(pref.LocalHour))
		if !due {
			continue
		}
		eligible, err := IsEligible(ctx, r.DB, pref.UserID)
		if err != nil {
			return s, err
		}
		if !eligible {
			continue
		}
		out, err := r.Run(ctx, pref, period)
		if err != nil {
			return s, err
		}
		switch out.Kind {
		case Sent:
			s.Claimed++
			s.Sent++
		case Failed:
			s.Claimed++
			s.Failed++
		case AlreadyClaimed:
		}
	}
	return s, nil
}

// Run claims, composes and delivers one user's digest for period.
func (r *Runner) Run(ctx context.Context, pref *digestdb.DigestPreference, period Period) (Outcome, error) {
	userID := pref.UserID
	digestID, claimed, err := r.ClaimDigest(ctx, userID, period)
	if err != nil {
		return Outcome{}, err
	}
	if !claimed {
		return Outcome{Kind: AlreadyClaimed}, nil
	}

	payload, err := r.compose(ctx, digestID, pref, period)
	if err != nil {
		r.Log.Error("digest.compose_failed", "user", userID, "period", period.Start, "error", err)
		msg := truncate(err.Error(), 500)
		now := r.now()
		if err := r.q().DigestSetFailed(ctx, digestdb.DigestSetFailedParams{ID: digestID, Error: &msg, UpdatedAt: &now}); err != nil {
			return Outcome{}, err
		}
		return Outcome{Kind: Failed, DigestID: digestID}, nil
	}

	delivered := false
	if r.Config.EmailEnabled && pref.EmailEnabled {
		delivered = r.deliverEmail(ctx, digestID, &payload, pref) || delivered
	}
	status := StatusFailed
	if delivered {
		status = StatusSent
	}
	now := r.now()
	if err := r.q().DigestSetStatus(ctx, digestdb.DigestSetStatusParams{ID: digestID, Status: status, UpdatedAt: &now}); err != nil {
		return Outcome{}, err
	}
	r.Log.Info("digest."+status, "user", userID, "period", period.Start, "digest", digestID)
	if delivered {
		return Outcome{Kind: Sent, DigestID: digestID}, nil
	}
	return Outcome{Kind: Failed, DigestID: digestID}, nil
}

func (r *Runner) compose(ctx context.Context, digestID uuid.UUID, pref *digestdb.DigestPreference, period Period) (Payload, error) {
	previous, err := r.previousPayload(ctx, pref.UserID, period.Start)
	if err != nil {
		return Payload{}, err
	}
	payload, err := Collector{DB: r.DB, Sources: r.Sources, Log: r.Log}.Collect(ctx, pref.UserID, period, pref, previous)
	if err != nil {
		return Payload{}, err
	}
	raw, err := apijson.Marshal(payload)
	if err != nil {
		return Payload{}, err
	}
	encoded := string(raw)
	now := r.now()
	if err := r.q().DigestSetComposed(ctx, digestdb.DigestSetComposedParams{ID: digestID, Payload: &encoded, UpdatedAt: &now}); err != nil {
		return Payload{}, err
	}
	return payload, nil
}

func (r *Runner) hasActiveChannel(pref *digestdb.DigestPreference) bool {
	return r.Config.EmailEnabled && pref.EmailEnabled
}

func (r *Runner) deliverEmail(ctx context.Context, digestID uuid.UUID, payload *Payload, pref *digestdb.DigestPreference) bool {
	ok, err := r.tryDeliverEmail(ctx, digestID, payload, pref)
	if err != nil {
		r.Log.Error("digest.email_failed", "digest", digestID, "error", err)
		return false
	}
	return ok
}

func (r *Runner) tryDeliverEmail(ctx context.Context, digestID uuid.UUID, payload *Payload, pref *digestdb.DigestPreference) (bool, error) {
	q := r.q()
	ids, err := q.DeliveryClaim(ctx, digestdb.DeliveryClaimParams{ID: uuid.New(), DigestID: digestID, Channel: ChannelEmail})
	if err != nil || len(ids) == 0 {
		return false, err
	}
	deliveryID := ids[0]
	users, err := q.DigestUserEmail(ctx, pref.UserID)
	if err != nil || len(users) == 0 {
		return false, err
	}
	user := users[0]
	// Unverified addresses bounce, and bounces cost the domain that also
	// delivers MFA codes.
	if user.EmailVerifiedAt == nil {
		return false, r.finishDelivery(ctx, deliveryID, DeliveryFailed, "email_unverified")
	}
	rendered := Renderer{WebBaseURL: r.Config.WebBaseURL, APIBaseURL: r.Config.APIBaseURL}.Render(digestID, payload, pref.UnsubscribeToken)
	html := rendered.HTML
	if err := r.Mailer.Send(ctx, mailer.Message{
		To:       user.Email,
		Subject:  rendered.Subject,
		Body:     rendered.Text,
		Purpose:  "weekly_digest",
		HTMLBody: &html,
		Headers:  rendered.Headers,
	}); err != nil {
		return false, r.finishDelivery(ctx, deliveryID, DeliveryFailed, err.Error())
	}
	return true, r.finishDelivery(ctx, deliveryID, DeliverySent, "")
}

func (r *Runner) finishDelivery(ctx context.Context, id uuid.UUID, status, msg string) error {
	var errText *string
	if status != DeliverySent {
		t := truncate(msg, 500)
		errText = &t
	}
	var sentAt *time.Time
	if status == DeliverySent {
		now := r.now()
		sentAt = &now
	}
	return r.q().DeliveryFinish(ctx, digestdb.DeliveryFinishParams{ID: id, Status: status, Error: errText, SentAt: sentAt})
}

// ClaimDigest inserts the week's row; claimed is false when it exists.
func (r *Runner) ClaimDigest(ctx context.Context, userID uuid.UUID, period Period) (uuid.UUID, bool, error) {
	ids, err := r.q().DigestClaim(ctx, digestdb.DigestClaimParams{ID: uuid.New(), UserID: userID, PeriodStart: period.Start, PeriodEnd: period.End})
	if err != nil || len(ids) == 0 {
		return uuid.UUID{}, false, err
	}
	return ids[0], true, nil
}

func (r *Runner) previousPayload(ctx context.Context, userID uuid.UUID, before string) (*Payload, error) {
	rows, err := r.q().DigestPreviousPayload(ctx, digestdb.DigestPreviousPayloadParams{UserID: userID, PeriodStart: before})
	if err != nil || len(rows) == 0 || rows[0] == nil {
		return nil, err
	}
	return decodePayload(*rows[0])
}

// decodePayload is WeeklyDigest.decodedPayload().
func decodePayload(raw string) (*Payload, error) {
	var p Payload
	if err := apijson.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("decode digest payload: %w", err)
	}
	p.normalize()
	return &p, nil
}

// SendablePreference is DigestSendCommand.sendablePreference: the user's
// preferences, provided the scheduled job would send to them too.
func SendablePreference(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID) (*digestdb.DigestPreference, error) {
	rows, err := digestdb.New(db).DigestPreferenceForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("user has no digest preferences; PUT /v1/digest-preferences first")
	}
	ok, err := IsEligible(ctx, db, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("user holds no positions and has no active bank connection; the digest would be empty")
	}
	return &rows[0], nil
}

// truncate is String(s.prefix(n)) by runes.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// loadTimeZone is TimeZone(identifier:): an IANA name ("" and "Local" are
// not identifiers).
func loadTimeZone(name string) (*time.Location, bool) {
	if name == "" || name == "Local" {
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	return loc, err == nil
}
