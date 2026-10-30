package digest

import (
	"norviq-api/internal/platform/swiftjson"

	"github.com/labstack/echo/v5"
)

// preferencesUpdate is DigestPreferencesUpdate: every field optional, a PUT
// changes only what it names.
type preferencesUpdate struct {
	EmailEnabled           *bool   `json:"emailEnabled"`
	PushEnabled            *bool   `json:"pushEnabled"`
	TelegramEnabled        *bool   `json:"telegramEnabled"`
	SentimentAlertsEnabled *bool   `json:"sentimentAlertsEnabled"`
	Weekday                *int    `json:"weekday"`
	LocalHour              *int    `json:"localHour"`
	Timezone               *string `json:"timezone"`
	Country                *string `json:"country"`
}

// decodePreferencesUpdate is req.content.decode(DigestPreferencesUpdate.self).
func decodePreferencesUpdate(c *echo.Context) (preferencesUpdate, error) {
	var u preferencesUpdate
	err := swiftjson.DecodeRequest(c, &u)
	return u, err
}
