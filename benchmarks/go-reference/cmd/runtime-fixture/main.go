package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
 "io"
	"github.com/pauljones0/rfd-discord-bot/internal/api"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/processor"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
	"github.com/pauljones0/rfd-discord-bot/internal/validator"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"time"
)

type localTransport struct {
	base *url.URL
	next http.RoundTripper
}

func (t localTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.base.Scheme
	u.Host = t.base.Host
	c.URL = &u
	c.Host = t.base.Host
	return t.next.RoundTrip(c)
}
func must(e error) {
	if e != nil {
		panic(e)
	}
}
func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := os.Args[1]
	path := os.Args[2]
	u, e := url.Parse(base)
	must(e)
	http.DefaultTransport = localTransport{u, http.DefaultTransport.(*http.Transport).Clone()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, e := storage.Open(ctx, path)
	must(e)
	defer store.Close()
	must(store.BindApplication(ctx, "1001"))
	data, e := os.ReadFile(os.Args[3])
	must(e)
	var deals []models.DealInfo
	must(json.Unmarshal(data, &deals))
	must(store.BatchWrite(ctx, deals, nil))
	must(store.SaveSubscription(ctx, models.Subscription{GuildID: "1", ChannelID: "42", SubscriptionType: "rfd", DealType: "rfd_all"}))
	cfg := &config.Config{DiscordAppID: "1001", AllowedDomains: []string{"127.0.0.1"}, RFDBaseURL: base, MaxStoredDeals: 2000, DiscordUpdateInterval: 10 * time.Minute}
	selectors, e := scraper.LoadSelectorsFromBytes([]byte(`{
    "hot_deals_list": {
        "container": {
            "item": "li.topic-card.topic",
            "ignore_modifier": ".sticky, :has(.sponsored-offer)"
        },
        "elements": {
            "title_link": "a.topic-card-info.thread_info",
            "title_text": ".thread_title",
            "retailer": ".thread_dealer",
            "posted_time": "time.topic_time",
            "author_link": "",
            "author_name": "",
            "thread_image": ".thread_image img",
            "like_count": ".thread_extra_info .votes",
            "comment_count": ".thread_extra_info .posts",
            "comment_count_fallback": ".posts_count",
            "view_count": ""
        }
    },
    "deal_details": {
        "primary_link": ".deal_link a",
        "fallback_link": ".postlink",
        "category": ".thread_category"
    }
}
`))
	must(e)
	handler := api.NewHandler(store)
	processor := processor.New(store, notifier.New("fixture-only", "1001"), scraper.NewWithBaseURL(cfg, selectors, base), validator.New(), cfg, nil)
	gateway, e := api.NewGateway("fixture-only", handler)
	must(e)
	done := make(chan struct{})
	go func() { gateway.Run(ctx); close(done) }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, httptest.NewRequest("GET", "/discord", nil))
		if w.Code == 200 {
			break
		}
		if time.Now().After(deadline) {
			panic("fixture Gateway not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	poll := func() {
		c, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		must(processor.ProcessDeals(c))
	}
	for range 3 {
		poll()
	}
	fmt.Println(`{"phase":"ready"}`)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var command struct {
			Polls int `json:"polls"`
		}
		must(json.Unmarshal(scanner.Bytes(), &command))
		for range command.Polls {
			poll()
		}
		deals, e := store.GetRecentDeals(ctx, 48*time.Hour)
		must(e)
		if len(deals) != 2000 {
			panic(fmt.Sprintf("wrong deal count %d", len(deals)))
		}
		fmt.Println(`{"phase":"done"}`)
	}
	must(scanner.Err())
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		panic("Gateway shutdown timeout")
	}
}
