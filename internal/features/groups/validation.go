package groups

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"norviq-api/internal/httpx"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/width"
)

// GroupsValidation limits.
const (
	maxGroupsCreatedPerDay = 3
	maxGroupsOwned         = 10
	maxGroupsJoined        = 100
	maxMembers             = 250
	maxChannels            = 20
	maxMessageLength       = 2000
	maxMessagesPerDay      = 600
	maxActiveInviteLinks   = 25
	maxDirectInvitations   = 25
	maxInviteHours         = 24 * 30
	maxInviteUses          = 1000
	defaultChannelName     = "general"
)

func trim(s string) string { return strings.TrimSpace(s) }

// validateGroupName is GroupsValidation.groupName.
func validateGroupName(raw string) (string, error) {
	name := strings.Join(strings.Fields(raw), " ")
	if n := charCount(name); n < 2 || n > 50 {
		return "", httpx.BadRequest("Group name must be 2–50 characters.")
	}
	if err := checkContent(name); err != nil {
		return "", err
	}
	return name, nil
}

// validateGroupDescription is GroupsValidation.groupDescription.
func validateGroupDescription(raw *string) (string, error) {
	d := ""
	if raw != nil {
		d = trim(*raw)
	}
	if charCount(d) > 300 {
		return "", httpx.BadRequest("Keep the description under 300 characters.")
	}
	if err := checkContent(d); err != nil {
		return "", err
	}
	return d, nil
}

// validateIconEmoji is GroupsValidation.iconEmoji: one emoji, or nil.
func validateIconEmoji(raw *string) (*string, error) {
	icon := ""
	if raw != nil {
		icon = trim(*raw)
	}
	if icon == "" {
		return nil, nil
	}
	g := graphemes(icon)
	if len(g) != 1 || !isEmojiCharacter(g[0]) {
		return nil, httpx.BadRequest("The icon must be a single emoji.")
	}
	return &icon, nil
}

// validateTicker is GroupsValidation.ticker: `$aapl` -> `AAPL`; empty is nil.
func validateTicker(raw *string, topic string) (*string, error) {
	t := ""
	if raw != nil {
		t = strings.ToUpper(trim(*raw))
	}
	t = strings.TrimPrefix(t, "$")
	if t == "" {
		return nil, nil
	}
	if !topicAllowsTicker(topic) {
		return nil, httpx.BadRequest("Only stock, ETF and crypto groups take a ticker.")
	}
	if !isTicker(t) {
		return nil, httpx.BadRequest("That doesn't look like a ticker.")
	}
	return &t, nil
}

func isTicker(t string) bool {
	g := graphemes(t)
	if len(g) < 1 || len(g) > 12 {
		return false
	}
	if c := g[0]; len(c) != 1 || !isASCIIAlnum(c[0]) {
		return false
	}
	for _, c := range g {
		if len(c) != 1 {
			return false
		}
		b := c[0]
		if !((b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '.' || b == '-') {
			return false
		}
	}
	return true
}

func isASCIIAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// validateChannelName is GroupsValidation.channelName: "Earnings Talk" ->
// "earnings-talk".
func validateChannelName(raw string) (string, error) {
	lowered := strings.ToLower(trim(raw))
	var b strings.Builder
	last := byte(0)
	for _, c := range graphemes(lowered) {
		if c == " " || c == "_" || c == "-" {
			if b.Len() > 0 && last != '-' {
				b.WriteByte('-')
				last = '-'
			}
			continue
		}
		b.WriteString(c)
		last = c[len(c)-1]
	}
	name := strings.TrimRight(b.String(), "-")
	allowed := true
	for _, c := range graphemes(name) {
		if len(c) != 1 || !((c[0] >= 'a' && c[0] <= 'z') || (c[0] >= '0' && c[0] <= '9') || c[0] == '-') {
			allowed = false
			break
		}
	}
	if n := charCount(name); n < 1 || n > 32 || !allowed {
		return "", httpx.BadRequest("Channel names are 1–32 lowercase letters, numbers or dashes.")
	}
	if err := checkContent(name); err != nil {
		return "", err
	}
	return name, nil
}

// validateChannelTopic is GroupsValidation.channelTopic.
func validateChannelTopic(raw *string) (*string, error) {
	t := ""
	if raw != nil {
		t = trim(*raw)
	}
	if t == "" {
		return nil, nil
	}
	if charCount(t) > 200 {
		return nil, httpx.BadRequest("Keep the channel topic under 200 characters.")
	}
	if err := checkContent(t); err != nil {
		return nil, err
	}
	return &t, nil
}

// validateMessageBody is GroupsValidation.messageBody.
func validateMessageBody(raw string) (string, error) {
	body := trim(raw)
	if body == "" {
		return "", httpx.BadRequest("Write something first.")
	}
	if charCount(body) > maxMessageLength {
		return "", httpx.BadRequest("Messages are limited to " + strconv.Itoa(maxMessageLength) + " characters.")
	}
	if err := checkContent(body); err != nil {
		return "", err
	}
	return body, nil
}

// validateClientMessageID is GroupsValidation.clientMessageId.
func validateClientMessageID(raw string) (string, error) {
	id := trim(raw)
	if n := charCount(id); n < 1 || n > 64 {
		return "", httpx.BadRequest("clientMessageId must be 1–64 characters.")
	}
	return id, nil
}

// inviteOptions is CreateGroupInviteRequest; nil hours never expire, nil
// uses is unlimited.
type inviteOptions struct {
	ExpiresInHours *int
	MaxUses        *int
}

// defaultInvite is CreateGroupInviteRequest.default: seven days, unlimited.
func defaultInvite() inviteOptions {
	h := 168
	return inviteOptions{ExpiresInHours: &h}
}

// validateInvite is GroupsValidation.invite.
func validateInvite(r inviteOptions, now time.Time) (*time.Time, *int, error) {
	if r.ExpiresInHours != nil && (*r.ExpiresInHours < 1 || *r.ExpiresInHours > maxInviteHours) {
		return nil, nil, httpx.BadRequest("Invites last between 1 hour and 30 days, or never expire.")
	}
	if r.MaxUses != nil && (*r.MaxUses < 1 || *r.MaxUses > maxInviteUses) {
		return nil, nil, httpx.BadRequest("An invite can be used 1–" + strconv.Itoa(maxInviteUses) + " times, or without limit.")
	}
	var expires *time.Time
	if r.ExpiresInHours != nil {
		t := now.Add(time.Duration(*r.ExpiresInHours) * time.Hour)
		expires = &t
	}
	return expires, r.MaxUses, nil
}

// ---- Content filter (GroupsContentFilter) ----------------------------------

// builtInTerms are slurs only. Ordinary profanity is allowed.
var builtInTerms = []string{
	"nigger", "nigga", "faggot", "kike", "chink", "gook",
	"wetback", "tranny", "raghead", "towelhead",
}

var builtInPatterns = compileTerms(builtInTerms)

func compileTerms(terms []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(terms))
	for _, t := range terms {
		if p := termPattern(t); p != nil {
			out = append(out, p)
		}
	}
	return out
}

// checkContent is GroupsContentFilter.check with GROUPS_BLOCKED_TERMS.
func checkContent(text string) error {
	if !contentAllowed(text, environmentTerms()) {
		return httpx.CodedAbort(http.StatusUnprocessableEntity, "content_rejected",
			"This wasn't sent because it may break the community guidelines.")
	}
	return nil
}

// contentAllowed is GroupsContentFilter.isAllowed.
func contentAllowed(text string, extraTerms []string) bool {
	haystack := normalizeForFilter(text)
	if haystack == "" {
		return true
	}
	for _, p := range builtInPatterns {
		if p.MatchString(haystack) {
			return false
		}
	}
	for _, p := range compileTerms(extraTerms) {
		if p.MatchString(haystack) {
			return false
		}
	}
	return true
}

func environmentTerms() []string {
	var out []string
	for _, part := range strings.Split(os.Getenv("GROUPS_BLOCKED_TERMS"), ",") {
		if p := trim(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var leet = map[string]byte{"0": 'o', "1": 'i', "3": 'e', "4": 'a', "5": 's', "7": 't', "@": 'a', "$": 's'}

var foldDiacritics = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// normalizeForFilter is GroupsContentFilter.normalize: lowercase ASCII words
// separated by single spaces, with leetspeak mapped back to letters.
func normalizeForFilter(text string) string {
	folded, _, err := transform.String(foldDiacritics, width.Fold.String(text))
	if err != nil {
		folded = text
	}
	folded = strings.ToLower(folded)
	var b strings.Builder
	lastSpace := false
	for _, c := range graphemes(folded) {
		ch := byte(0)
		if l, ok := leet[c]; ok {
			ch = l
		} else if len(c) == 1 && c[0] >= 'a' && c[0] <= 'z' {
			ch = c[0]
		}
		if ch != 0 {
			b.WriteByte(ch)
			lastSpace = false
		} else if b.Len() > 0 && !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// termPattern matches a multi-word term across single spaces; each letter
// may repeat.
func termPattern(term string) *regexp.Regexp {
	words := strings.Fields(normalizeForFilter(term))
	if len(words) == 0 {
		return nil
	}
	parts := make([]string, len(words))
	for i, w := range words {
		var b strings.Builder
		for _, r := range w {
			b.WriteString(regexp.QuoteMeta(string(r)))
			b.WriteByte('+')
		}
		parts[i] = b.String()
	}
	p, err := regexp.Compile("(?:^| )" + strings.Join(parts, " ") + "(?: |$)")
	if err != nil {
		return nil
	}
	return p
}

// ---- Invite codes (InviteCode) ---------------------------------------------

const inviteAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

func generateInviteCode() string {
	b := make([]byte, 10)
	max := big.NewInt(int64(len(inviteAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			b[i] = 'x'
			continue
		}
		b[i] = inviteAlphabet[n.Int64()]
	}
	return string(b)
}

// inviteCodeWellFormed is InviteCode.isWellFormed.
func inviteCodeWellFormed(code string) bool {
	g := graphemes(code)
	if len(g) < 4 || len(g) > 64 {
		return false
	}
	for _, c := range g {
		if len(c) != 1 || !(isASCIIAlnum(c[0]) || c[0] == '-' || c[0] == '_') {
			return false
		}
	}
	return true
}

// ---- Characters --------------------------------------------------------------

// charCount approximates Swift's String.count (extended grapheme clusters).
func charCount(s string) int { return len(graphemes(s)) }

// prefixChars is Swift's String(s.prefix(n)).
func prefixChars(s string, n int) string {
	g := graphemes(s)
	if len(g) <= n {
		return s
	}
	return strings.Join(g[:n], "")
}

// graphemes splits s the way boards does (validation.go there): combining
// marks, variation selectors, emoji modifiers, tags and ZWJ sequences attach
// to the character before them; a regional-indicator pair is one flag.
func graphemes(s string) []string {
	var out []string
	var cur []rune
	joinNext := false
	riOpen := false
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		extends := len(cur) > 0 && (joinNext || isExtender(r) ||
			(r == '\n' && cur[len(cur)-1] == '\r' && len(cur) == 1) ||
			(isRegionalIndicator(r) && riOpen))
		if !extends {
			flush()
			riOpen = false
		}
		switch {
		case isRegionalIndicator(r) && !extends:
			riOpen = true
		case isRegionalIndicator(r):
			riOpen = false
		}
		joinNext = r == 0x200D
		cur = append(cur, r)
	}
	flush()
	return out
}

func isExtender(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r) ||
		(r >= 0xFE00 && r <= 0xFE0F) || (r >= 0x1F3FB && r <= 0x1F3FF) || r == 0x200D ||
		(r >= 0xE0020 && r <= 0xE007F)
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }
