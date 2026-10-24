package jobs

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// WhoAmI answers every message the bot gets with the IDs of that chat and its sender, so
// the owner can find the TELEGRAM_ALERT_CHAT to put in the settings: write to the bot in a
// private chat, or add it to a group and send /id there (in groups the bot only sees
// commands). Runs until ctx ends. Only one program may read a bot's messages at a time: a
// second one (the laptop and the server on the same token) gets 409s, logged and retried.
func (a Telegram) WhoAmI(ctx context.Context) {
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
				slog.Warn("telegram whoami", "err", err)
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
			form := url.Values{"chat_id": {strconv.FormatInt(m.Chat.ID, 10)}, "text": {whoAmIText(m)}, "parse_mode": {"HTML"}}
			if m.ThreadID != 0 {
				form.Set("message_thread_id", strconv.Itoa(m.ThreadID))
			}
			if err := a.call(ctx, "sendMessage", form, nil); err != nil {
				slog.Warn("telegram whoami reply", "chat", m.Chat.ID, "err", err)
			}
		}
		if offset == -1 { // nothing was waiting: from now on, everything new
			offset = 0
		}
	}
}

type tgUpdate struct {
	ID          int        `json:"update_id"`
	Message     *tgMessage `json:"message"`
	ChannelPost *tgMessage `json:"channel_post"`
}

type tgMessage struct {
	ThreadID int `json:"message_thread_id"`
	Chat     struct {
		ID        int64  `json:"id"`
		Type      string `json:"type"`
		Title     string `json:"title"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	} `json:"chat"`
	From *struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
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
