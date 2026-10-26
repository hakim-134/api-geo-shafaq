package jobs

import (
	"context"
	"errors"
	"github.com/hakim-134/api-geo-shafaq/internal/auth"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakim-134/api-geo-shafaq/internal/provider"
	"github.com/hakim-134/api-geo-shafaq/internal/store"
	"github.com/hakim-134/api-geo-shafaq/internal/testdb"
)

type sent []string

func (s *sent) Send(_ context.Context, text string) error {
	*s = append(*s, text)
	return nil
}

// cabinet answers like Yandex's Developer's Account API, with used of 100 requests today.
func cabinet(t *testing.T, used *int) provider.YandexCabinet {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"projects":[{"id":"p1"}]}`))
	})
	mux.HandleFunc("GET /projects/p1/services", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"services":[{"id":"s1","name":"JavaScript API"}]}`))
	})
	mux.HandleFunc("GET /projects/p1/services/s1/limits", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"limits":{"panorama_daily":{"limit":100,"value":` + strconv.Itoa(*used) + `}}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return provider.YandexCabinet{Key: "k", Base: srv.URL, Client: srv.Client()}
}

func TestQuotaCheckWarnsOnceADay(t *testing.T) {
	ctx := context.Background()
	q := store.New(testdb.New(t))
	used := 50
	var got sent
	quota := &QuotaCheck{Q: q, Cabinet: cabinet(t, &used), Alert: &got}
	ours := &UsageCheck{Q: q, Limit: 100, Alert: &got}

	if err := quota.Work(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("50 of 100: err %v, sent %q", err, got)
	}
	used = 85
	for range 2 {
		if err := quota.Work(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || !strings.Contains(got[0], "85 of 100") {
		t.Fatalf("85 of 100, checked twice: sent %q", got)
	}

	// our own count reaching 80% the same day says nothing more
	for range 90 {
		if _, err := q.CountProviderView(ctx, store.CountProviderViewParams{Day: provider.Today(time.Now()), Provider: "yandex"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ours.Work(ctx, nil); err != nil || len(got) != 1 {
		t.Fatalf("second warning the same day: err %v, sent %q", err, got)
	}
}

func TestUsageCheckWarnsOnce(t *testing.T) {
	ctx := context.Background()
	q := store.New(testdb.New(t))
	var got sent
	w := &UsageCheck{Q: q, Limit: 100, Alert: &got}
	day := store.CountProviderViewParams{Day: provider.Today(time.Now()), Provider: "yandex"}
	for i := 1; i <= 80; i++ {
		if _, err := q.CountProviderView(ctx, day); err != nil {
			t.Fatal(err)
		}
		if i == 79 || i == 80 {
			if err := w.Work(ctx, nil); err != nil {
				t.Fatal(err)
			}
			if want := i - 79; len(got) != want {
				t.Fatalf("after %d views: sent %d, want %d", i, len(got), want)
			}
		}
	}
	if err := w.Work(ctx, nil); err != nil || len(got) != 1 {
		t.Fatalf("checked again: err %v, sent %q", err, got)
	}
}

func TestUsageCheckTellsAboutACap(t *testing.T) {
	ctx := context.Background()
	q := store.New(testdb.New(t))
	var got sent
	w := &UsageCheck{Q: q, Limit: 100, Caps: map[string]int{"google": 10}, Alert: &got}
	day := store.CountProviderViewParams{Day: provider.Today(time.Now()), Provider: "google"}
	check := func(views, want int) {
		t.Helper()
		for range views {
			if _, err := q.CountProviderView(ctx, day); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Work(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if len(got) != want {
			t.Fatalf("sent %q, want %d messages", got, want)
		}
	}
	check(7, 0)
	check(1, 1) // 8 of 10
	if !strings.Contains(got[0], "8 google panorama views") || !strings.Contains(got[0], "cap of 10") {
		t.Fatalf("near the cap: %q", got[0])
	}
	check(2, 2) // 10 of 10: stopped
	if !strings.Contains(got[1], "google reached today's cap of 10") {
		t.Fatalf("at the cap: %q", got[1])
	}
	check(5, 2) // told once a day

	// a cap set later says nothing about old days
	old := store.CountProviderViewParams{Day: provider.Today(time.Now()).AddDate(0, 0, -3), Provider: "yandex"}
	for range 150 {
		if _, err := q.CountProviderView(ctx, old); err != nil {
			t.Fatal(err)
		}
	}
	w.Caps["yandex"] = 100
	check(0, 2)
}

func TestTelegramErrorHidesToken(t *testing.T) {
	tg := Telegram{Token: "secret-token", Chat: "1", Client: &http.Client{Transport: failing{}}}
	err := tg.Send(context.Background(), "hi")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error %v", err)
	}
}

type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestBotAnswersGroupsWithChatID(t *testing.T) {
	replies := make(chan string, 1)
	polls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bott/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls > 1 {
			w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":[{"update_id":7,"message":{"chat":{"id":-100123,"type":"supergroup","title":"Shafaq <alerts>"},` +
			`"from":{"id":42,"first_name":"Ali","username":"ali"}}}]}`))
	})
	mux.HandleFunc("POST /bott/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("chat_id") != "-100123" {
			t.Errorf("replied to chat %q", r.Form.Get("chat_id"))
		}
		replies <- r.Form.Get("text")
		w.Write([]byte(`{"ok":true,"result":{}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Telegram{Token: "t", Base: srv.URL, Client: srv.Client()}.Run(ctx, Bot{})
	select {
	case text := <-replies:
		for _, want := range []string{"<code>-100123</code>", "Shafaq &lt;alerts&gt;", "<code>42</code>", "TELEGRAM_ALERT_CHAT=-100123"} {
			if !strings.Contains(text, want) {
				t.Errorf("reply %q lacks %q", text, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply")
	}
}

func TestBotConfirmsSignIn(t *testing.T) {
	replies := make(chan string, 1)
	polls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bott/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls > 1 {
			w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":[{"update_id":8,"message":{"text":"/start login_abc","chat":{"id":42,"type":"private"},` +
			`"from":{"id":42,"first_name":"Ali","username":"ali","language_code":"ru"}}}]}`))
	})
	mux.HandleFunc("POST /bott/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		replies <- r.Form.Get("text")
		w.Write([]byte(`{"ok":true,"result":{}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var got auth.TelegramUser
	var code string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Telegram{Token: "t", Base: srv.URL, Client: srv.Client()}.Run(ctx, Bot{
		Confirm: func(_ context.Context, c string, u auth.TelegramUser) (bool, error) {
			code, got = c, u
			return true, nil
		},
	})
	select {
	case text := <-replies:
		if code != "abc" || got.ID != 42 || got.Username != "ali" {
			t.Errorf("confirmed %q for %+v", code, got)
		}
		if !strings.Contains(text, "Готово") {
			t.Errorf("reply %q is not the Russian confirmation", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply")
	}
}
