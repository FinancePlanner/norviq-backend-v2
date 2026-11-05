package assistant

import (
	"context"
	"strings"

	"norviq-api/internal/features/tax"
	"norviq-api/internal/platform/ai"

	"github.com/google/uuid"
)

// extendedToolsEnv switches on the read tools beyond what the older consent
// copy lists. It flips only after the compliance read.
const extendedToolsEnv = "ASSISTANT_EXTENDED_TOOLS_ENABLED"

// extendedReadToolDefinitions are the read tools restricted to in-app sessions
// that are not shared with a messaging chat: see extendedToolsAllowed. They
// stay out of readToolNames, so every other surface refuses them as unknown.
func extendedReadToolDefinitions() []ai.Tool {
	return []ai.Tool{
		readTool("get_financial_goals", "The user's savings goals with targets, progress, and whether each is on track.", nil),
		readTool("get_tax_summary", "The user's tax position for a jurisdiction and year: gains, losses, and estimated tax. Ask which country when it is unclear.", map[string]ai.Parameter{
			"jurisdiction": enumParam("Tax jurisdiction.", tax.JurisdictionCodes()),
			"tax_year":     param("integer", "Four-digit tax year. Defaults to the current year."),
		}, "jurisdiction"),
		readTool("get_retirement_projection", "The user's saved retirement plans: ages, income, spending, and accounts.", nil),
		readTool("get_planning_summary", "The user's saved financial-planning scenarios and the figures prefilled for new ones.", nil),
		readTool("get_rebalancing_status", "How far the user's portfolios have drifted from target, and any open rebalancing alerts.", nil),
		readTool("list_price_targets", "The user's stock price targets and alerts.", nil),
		readTool("list_research_notes", "The user's own recent research notes on stocks.", nil),
		readTool("get_latest_recap", "The user's most recent periodic recaps.", nil),
		readTool("list_achievements", "The achievements the user has unlocked and their progress on others.", nil),
		readTool("list_groups", "The names of the groups the user belongs to and their role in each. Never includes messages.", nil),
	}
}

var extendedToolSet = func() map[string]bool {
	out := map[string]bool{}
	for _, t := range extendedReadToolDefinitions() {
		out[t.Function.Name] = true
	}
	return out
}()

func isExtendedTool(name string) bool { return extendedToolSet[name] }

func extendedToolNames() []string {
	tools := extendedReadToolDefinitions()
	out := make([]string, 0, len(tools))
	for i := range tools {
		out = append(out, tools[i].Function.Name)
	}
	return out
}

// extendedToolsAllowed requires all of: the flag, a first-party session, a
// client that sends context (older apps show the older consent copy, and
// Telegram and scheduled checks send none), and a conversation that no
// messaging chat reads, since a later Telegram turn replays this thread. A
// failed lookup denies.
func (h *handlers) extendedToolsAllowed(ctx context.Context, scoped bool, client clientContext, conversationID uuid.UUID) bool {
	if !h.extendedTools || scoped || !client.present {
		return false
	}
	bound, err := h.q.ConversationHasMessagingLink(ctx, &conversationID)
	if err != nil {
		h.d.Logger.Warn("ai_extended_tools_link_lookup_failed", "error", err)
		return false
	}
	return !bound
}

// executeExtended runs an extended read tool. Callers have already checked
// that this turn may use it. Results include text other people wrote, such as
// group names, so all of it is marked untrusted.
func (r *readTools) executeExtended(ctx context.Context, name string, args actionArgs, userID uuid.UUID) (string, error) {
	var (
		out any
		err error
	)
	if name == "get_tax_summary" {
		out, err = r.taxSummary(ctx, args, userID)
	} else {
		read := r.src.ExtendedReads[name]
		if read == nil {
			return "", errSourceUnwired
		}
		out, err = read(ctx, userID)
	}
	if err != nil {
		return "", err
	}
	return untrustedPayload(out)
}

func (r *readTools) taxSummary(ctx context.Context, args actionArgs, userID uuid.UUID) (any, error) {
	if r.src.Tax == nil {
		return nil, errSourceUnwired
	}
	raw, _ := args.trimmedString("jurisdiction")
	jurisdiction, ok := tax.ParseJurisdiction(strings.ToUpper(raw))
	if !ok {
		return map[string]string{"error": "Ask the user which country's taxes they mean."}, nil
	}
	year := r.now().UTC().Year()
	if n, ok := args.readInt("tax_year"); ok {
		year = n
	}
	return r.src.Tax(ctx, userID, jurisdiction, year)
}

// ExtendedSourceNames lists the extended tools that need an ExtendedReads
// entry. get_tax_summary reads Sources.Tax instead.
func ExtendedSourceNames() []string {
	var out []string
	for _, name := range extendedToolNames() {
		if name != "get_tax_summary" {
			out = append(out, name)
		}
	}
	return out
}
