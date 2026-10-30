package telegram

import (
	"strconv"

	"norviq-api/internal/platform/swiftjson"
)

// Update is TelegramUpdate: the slice of Telegram's payload the bot acts on.
// Non-pointer fields are required, as in the Swift Decodable.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// Chat is TelegramUpdate.Chat.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// Audio is a voice note or an audio file (a file_id, never bytes).
type Audio struct {
	FileID   string  `json:"file_id"`
	Duration int     `json:"duration"`
	MimeType *string `json:"mime_type"`
	FileSize *int64  `json:"file_size"`
}

// Message is TelegramUpdate.Message.
type Message struct {
	MessageID *int64  `json:"message_id"`
	Chat      Chat    `json:"chat"`
	Text      *string `json:"text"`
	Voice     *Audio  `json:"voice"`
	Audio     *Audio  `json:"audio"`
}

// CallbackQuery is an inline button tap.
type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    *string  `json:"data"`
	Message *Message `json:"message"`
}

// DecodeUpdate is `JSONDecoder().decode(TelegramUpdate.self, from:)`.
func DecodeUpdate(body []byte) (Update, error) {
	var u Update
	err := swiftjson.DecodeExternal(body, &u)
	return u, err
}

// InboundMessage is one message from any platform, stripped of its shape.
// Button taps arrive here too, with the button's value in Text.
type InboundMessage struct {
	Platform        string
	ExternalID      string
	UpdateID        int64
	Text            string
	IsPrivateChat   bool
	CallbackQueryID *string
}

// VoiceNote is what the bot needs to fetch and meter a voice note.
type VoiceNote struct {
	FileID     string
	Duration   int
	MimeType   *string
	FileSize   *int64
	ExternalID string
	UpdateID   int64
}

// IntentKind is TelegramUpdate.Intent's case.
type IntentKind int

// Intent cases.
const (
	IntentIgnore IntentKind = iota
	IntentAnswer
	IntentVoice
	IntentLeave
)

// Intent is what to do with an update.
type Intent struct {
	Kind    IntentKind
	Inbound InboundMessage // IntentAnswer
	Voice   VoiceNote      // IntentVoice
	ChatID  string         // IntentLeave
}

// Intent is TelegramUpdate.intent: anything unrecognized is ignored.
func (u Update) Intent() Intent {
	var source *Message
	if u.CallbackQuery != nil && u.CallbackQuery.Message != nil {
		source = u.CallbackQuery.Message
	} else {
		source = u.Message
	}
	if source == nil {
		return Intent{Kind: IntentIgnore}
	}
	chatID := strconv.FormatInt(source.Chat.ID, 10)
	switch source.Chat.Type {
	case "private":
	case "group", "supergroup", "channel":
		return Intent{Kind: IntentLeave, ChatID: chatID}
	default:
		return Intent{Kind: IntentIgnore}
	}

	var raw *string
	if u.CallbackQuery != nil && u.CallbackQuery.Data != nil {
		raw = u.CallbackQuery.Data
	} else {
		raw = source.Text
	}
	text := ""
	if raw != nil {
		text = trimSwift(*raw)
	}
	if text == "" {
		clip := source.Voice
		if clip == nil {
			clip = source.Audio
		}
		if clip != nil {
			return Intent{Kind: IntentVoice, Voice: VoiceNote{
				FileID: clip.FileID, Duration: clip.Duration, MimeType: clip.MimeType, FileSize: clip.FileSize,
				ExternalID: chatID, UpdateID: u.UpdateID,
			}}
		}
		return Intent{Kind: IntentIgnore}
	}
	var callbackID *string
	if u.CallbackQuery != nil {
		id := u.CallbackQuery.ID
		callbackID = &id
	}
	return Intent{Kind: IntentAnswer, Inbound: InboundMessage{
		Platform: Platform, ExternalID: chatID, UpdateID: u.UpdateID, Text: text,
		IsPrivateChat: true, CallbackQueryID: callbackID,
	}}
}
