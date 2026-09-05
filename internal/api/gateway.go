package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Gateway receives interactions over an outbound authenticated WebSocket.
// DiscordGo owns heartbeats and session resumption; Run owns reconnect backoff
// and shutdown so a Discord outage does not restart unrelated scheduled jobs.
type Gateway struct {
	session      *discordgo.Session
	handler      *Handler
	disconnected chan struct{}
	ready        atomic.Bool
	received     atomic.Uint64
	responded    atomic.Uint64
	failed       atomic.Uint64
	lastReceived atomic.Int64
}

func NewGateway(token string, handler *Handler) (*Gateway, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("DISCORD_BOT_TOKEN is required")
	}
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, errors.New("could not initialize Discord Gateway session")
	}
	// Interactions do not require privileged intents or access to message text.
	s.Identify.Intents = 0
	s.StateEnabled = false
	s.ShouldReconnectOnError = false
	s.SyncEvents = true
	s.Client.Timeout = 10 * time.Second
	s.LogLevel = discordgo.LogError
	g := &Gateway{session: s, handler: handler, disconnected: make(chan struct{}, 1)}
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.Ready) { g.markReady("ready") })
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) { g.markReady("resumed") })
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		g.ready.Store(false)
		select {
		case g.disconnected <- struct{}{}:
		default:
		}
	})
	s.AddHandler(func(_ *discordgo.Session, event *discordgo.Event) {
		if event.Type == "INTERACTION_CREATE" {
			// Keep raw fields, including autocomplete, modal and component data.
			// Never block Gateway heartbeat processing on a command handler.
			body := append([]byte(nil), event.RawData...)
			deadline := time.Now().Add(2800 * time.Millisecond)
			go g.handleEvent(body, deadline)
		}
	})
	return g, nil
}

func (g *Gateway) markReady(state string) {
	g.ready.Store(true)
	slog.Info("Discord Gateway connected", "state", state, "transport", "outbound websocket")
}

func (g *Gateway) Run(ctx context.Context) {
	defer g.session.Close()
	defer g.ready.Store(false)
	delay := 5 * time.Second
	for ctx.Err() == nil {
		// Clear notifications from a previous close before opening again.
		select {
		case <-g.disconnected:
		default:
		}
		if err := g.session.Open(); err == nil {
			delay = 5 * time.Second
			select {
			case <-ctx.Done():
				return
			case <-g.disconnected:
				slog.Warn("Discord Gateway disconnected; reconnecting")
			}
		} else {
			// Raw client errors can contain URLs or credentials. Log a safe
			// classification, not an interaction token or response body.
			slog.Warn("Discord Gateway connection failed", "error", safeDiscordError(err), "retry_in", delay)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, time.Minute)
	}
}

func (g *Gateway) handleEvent(body []byte, deadline time.Time) {
	var envelope struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.ID == "" || envelope.Token == "" {
		g.failed.Add(1)
		slog.Error("Invalid Discord Gateway interaction envelope")
		return
	}
	g.received.Add(1)
	g.lastReceived.Store(time.Now().Unix())
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	w := &gatewayResponseWriter{header: make(http.Header), send: func(payload json.RawMessage) error {
		endpoint := discordgo.EndpointInteractionResponse(envelope.ID, envelope.Token)
		_, err := g.session.RequestWithBucketID(http.MethodPost, endpoint, payload, endpoint, discordgo.WithContext(ctx))
		if err != nil {
			return safeDiscordError(err)
		}
		g.responded.Add(1)
		return nil
	}}
	defer func() {
		if recover() != nil {
			g.failed.Add(1)
			slog.Error("Discord Gateway command handler panicked", "interaction_id", envelope.ID)
		}
	}()
	g.handler.handleInteraction(w, body)
	if w.err != nil || !w.sent {
		g.failed.Add(1)
		slog.Error("Discord Gateway interaction response failed", "interaction_id", envelope.ID, "error", w.err)
	}
}

func safeDiscordError(err error) error {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) && rest.Response != nil {
		return fmt.Errorf("Discord HTTP status %d", rest.Response.StatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("Discord request deadline exceeded")
	}
	return errors.New("Discord transport request failed")
}

// gatewayResponseWriter sends the initial response during Write, before a
// handler launches deferred follow-up work. Buffering until the handler returns
// would let a follow-up race ahead of Discord's initial acknowledgement.
type gatewayResponseWriter struct {
	header http.Header
	body   []byte
	status int
	sent   bool
	err    error
	send   func(json.RawMessage) error
}

func (w *gatewayResponseWriter) Header() http.Header { return w.header }
func (w *gatewayResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *gatewayResponseWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.sent {
		return 0, errors.New("initial Discord response already sent")
	}
	if w.status >= 400 {
		w.err = fmt.Errorf("interaction handler returned HTTP %d", w.status)
		return 0, w.err
	}
	w.body = append(w.body, p...)
	if !json.Valid(w.body) {
		return len(p), nil
	}
	w.sent = true
	w.err = w.send(json.RawMessage(w.body))
	if w.err != nil {
		return 0, w.err
	}
	return len(p), nil
}

// ServeHTTP reports transport health separately from database/scheduler health.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ready := g.ready.Load()
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	writeJSON(w, map[string]any{
		"transport": "gateway", "ready": ready,
		"received": g.received.Load(), "responded": g.responded.Load(),
		"failed": g.failed.Load(), "last_received_unix": g.lastReceived.Load(),
	})
}
