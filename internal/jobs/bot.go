package jobs

import (
	"context"
	"fmt"
	"github.com/hakim-134/api-geo-shafaq/internal/auth"
	"html"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Bot is what the players' bot does besides alerts.
type Bot struct {
	// Confirm: a player pressed Start on a "sign in with Telegram" link (start=login_<code>).
	// False when the code is unknown, used or too old.
	Confirm func(ctx context.Context, code string, u auth.TelegramUser) (bool, error)
	GameURL string // where the welcome message sends players
}

// Run reads the bot's messages until ctx ends:
//
//   - /start login_<code>: confirms a sign-in from the game;
//   - /start or any other private message: a short welcome with the game's link;
//   - /id, and anything in a group or channel (where the bot sees only commands): the IDs of
//     that chat and its sender, so the owner can find TELEGRAM_ALERT_CHAT.
//
// Only one program may read a bot's messages at a time: a second one (the laptop and the
// server on the same token) gets 409s, logged and retried.
func (a Telegram) Run(ctx context.Context, b Bot) {
	if a.Token == "" {
		return
	}
	// Start from the newest waiting message: answer it, not a backlog of old ones.
	offset := -1
	for ctx.Err() == nil {
		var updates []tgUpdate
		err := a.call(ctx, "getUpdates", url.Values{
			"offset":          {strconv.Itoa(offset)},
			"timeout":         {"50"},
			"allowed_updates": {`["message","channel_post"]`},
		}, &updates)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("telegram bot", "err", err)
				sleep(ctx, 10*time.Second)
			}
			continue
		}
		for _, u := range updates {
			offset = u.ID + 1
			m := u.Message
			if m == nil {
				m = u.ChannelPost
			}
			if m == nil {
				continue
			}
			form := url.Values{"chat_id": {strconv.FormatInt(m.Chat.ID, 10)}, "text": {a.answer(ctx, b, m)}, "parse_mode": {"HTML"}}
			if m.ThreadID != 0 {
				form.Set("message_thread_id", strconv.Itoa(m.ThreadID))
			}
			if err := a.call(ctx, "sendMessage", form, nil); err != nil {
				slog.Warn("telegram bot reply", "chat", m.Chat.ID, "err", err)
			}
		}
		if offset == -1 { // nothing was waiting: from now on, everything new
			offset = 0
		}
	}
}

func (a Telegram) answer(ctx context.Context, b Bot, m *tgMessage) string {
	cmd, arg, _ := strings.Cut(strings.TrimSpace(m.Text), " ")
	cmd, _, _ = strings.Cut(cmd, "@") // /id@tmp67bot in groups
	private := m.Chat.Type == "private"
	switch {
	case cmd == "/id" || !private:
		return whoAmIText(m)
	case cmd == "/start" && strings.HasPrefix(arg, "login_") && m.From != nil && b.Confirm != nil:
		ok, err := b.Confirm(ctx, strings.TrimPrefix(arg, "login_"), auth.TelegramUser{
			ID: m.From.ID, FirstName: m.From.FirstName, LastName: m.From.LastName, Username: m.From.Username,
		})
		if err != nil {
			slog.Error("telegram sign-in", "err", err)
		}
		if ok {
			return botText(m, "signedIn")
		}
		return botText(m, "expired")
	default:
		return botText(m, "welcome") + "\n\n" + html.EscapeString(b.GameURL)
	}
}

// The bot speaks the player's Telegram language: Russian or English if Telegram says so,
// Uzbek otherwise.
var botTexts = map[string]map[string]string{
	"uz": {
		"signedIn": "✅ Tayyor! O‘yinga qayting: kirish tasdiqlandi.",
		"expired":  "Bu havola eskirgan. O‘yinda «Telegram orqali kirish» tugmasini qayta bosing.",
		"welcome":  "Salom! Bu <b>Shafaq Geo</b>: ko‘chaga tushasiz, atrofga qaraysiz va xaritada qayerdaligingizni topasiz.",
	},
	"ru": {
		"signedIn": "✅ Готово! Вернитесь в игру: вход подтверждён.",
		"expired":  "Эта ссылка устарела. Нажмите «Войти через Telegram» в игре ещё раз.",
		"welcome":  "Привет! Это <b>Shafaq Geo</b>: вы оказываетесь на улице, осматриваетесь и находите это место на карте.",
	},
	"en": {
		"signedIn": "✅ Done! Go back to the game: you are signed in.",
		"expired":  "This link has expired. Tap “Sign in with Telegram” in the game again.",
		"welcome":  "Hi! This is <b>Shafaq Geo</b>: you land on a street, look around and find the spot on the map.",
	},
}

func botText(m *tgMessage, key string) string {
	lang := "uz"
	if m.From != nil {
		switch {
		case strings.HasPrefix(m.From.LanguageCode, "ru"):
			lang = "ru"
		case strings.HasPrefix(m.From.LanguageCode, "en"):
			lang = "en"
		}
	}
	return botTexts[lang][key]
}

type tgUpdate struct {
	ID          int        `json:"update_id"`
	Message     *tgMessage `json:"message"`
	ChannelPost *tgMessage `json:"channel_post"`
}

type tgMessage struct {
	ThreadID int    `json:"message_thread_id"`
	Text     string `json:"text"`
	Chat     struct {
		ID        int64  `json:"id"`
		Type      string `json:"type"`
		Title     string `json:"title"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	} `json:"chat"`
	From *struct {
		ID           int64  `json:"id"`
		Username     string `json:"username"`
		FirstName    string `json:"first_name"`
		LastName     string `json:"last_name"`
		LanguageCode string `json:"language_code"`
	} `json:"from"`
}

func whoAmIText(m *tgMessage) string {
	c := m.Chat
	var b strings.Builder
	fmt.Fprintf(&b, "Chat ID: <code>%d</code>\n\n", c.ID)
	name := strings.TrimSpace(c.Title + " " + c.FirstName + " " + c.LastName)
	fmt.Fprintf(&b, "Chat: %s (%s)", html.EscapeString(name), c.Type)
	if c.Username != "" {
		fmt.Fprintf(&b, " @%s", html.EscapeString(c.Username))
	}
	if m.ThreadID != 0 {
		fmt.Fprintf(&b, "\nTopic ID: <code>%d</code>", m.ThreadID)
	}
	if f := m.From; f != nil {
		fmt.Fprintf(&b, "\nYou: %s, user ID <code>%d</code>",
			html.EscapeString(strings.TrimSpace(f.FirstName+" "+f.LastName)), f.ID)
		if f.Username != "" {
			fmt.Fprintf(&b, " @%s", html.EscapeString(f.Username))
		}
	}
	fmt.Fprintf(&b, "\n\nFor alerts here: <code>TELEGRAM_ALERT_CHAT=%d</code>", c.ID)
	return b.String()
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
