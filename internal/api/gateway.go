package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	connection   atomic.Pointer[gatewayConnection]
	openTimeout  time.Duration
}

// Open holds DiscordGo's session lock while awaiting HELLO and READY, so
// Session.Close cannot interrupt a stalled handshake. Own the underlying
// connection instead, with cancellation and a timer that stops once ready.
type gatewayConnection struct {
	net.Conn
	stopCancellation func() bool
	handshakeTimer   *time.Timer
}

func (c *gatewayConnection) Close() error {
	c.stopCancellation()
	c.handshakeTimer.Stop()
	return c.Conn.Close()
}

func NewGateway(token string, handler *Handler) (*Gateway, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("DISCORD_BOT_TOKEN is required")
	}
	s, err := newSession(token)
	if err != nil {
		return nil, errors.New("could not initialize Discord Gateway session")
	}
	// Interactions do not require privileged intents or access to message text.
	s.Identify.Intents = 0
	s.StateEnabled = false
	s.ShouldReconnectOnError = false
	s.SyncEvents = true
	s.LogLevel = discordgo.LogError
	g := &Gateway{session: s, handler: handler, disconnected: make(chan struct{}, 1), openTimeout: 20 * time.Second}
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
			// Never block Gateway heartbeat processing on a command handler.
			body := append([]byte(nil), event.RawData...)
			deadline := time.Now().Add(2800 * time.Millisecond)
			go g.handleEvent(body, deadline)
		}
	})
	return g, nil
}

func (g *Gateway) markReady(state string) {
	if conn := g.connection.Load(); conn != nil {
		conn.handshakeTimer.Stop()
	}
	g.ready.Store(true)
	slog.Info("Discord Gateway connected", "state", state, "transport", "outbound websocket")
}

func (g *Gateway) Run(ctx context.Context) {
	dialer := *g.session.Dialer
	dialer.HandshakeTimeout = g.openTimeout
	dialer.NetDialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		dialCtx, cancel := context.WithCancel(dialCtx)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		conn, err := (&net.Dialer{}).DialContext(dialCtx, network, address)
		if err != nil {
			return nil, err
		}
		owned := &gatewayConnection{
			Conn:             conn,
			stopCancellation: context.AfterFunc(ctx, func() { conn.Close() }),
			handshakeTimer:   time.AfterFunc(g.openTimeout, func() { conn.Close() }),
		}
		g.connection.Store(owned)
		return owned, nil
	}
	g.session.Dialer = &dialer
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
	var req interactionRequest
	if json.Unmarshal(body, &req) != nil || req.ID == "" || req.Token == "" {
		g.failed.Add(1)
		slog.Error("Invalid Discord Gateway interaction envelope")
		return
	}
	g.received.Add(1)
	g.lastReceived.Store(time.Now().Unix())
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	defer func() {
		if recover() != nil {
			g.failed.Add(1)
			slog.Error("Discord Gateway command handler panicked", "interaction_id", req.ID)
		}
	}()
	response := privateReply(g.handler.handleInteraction(ctx, req))
	endpoint := discordgo.EndpointInteractionResponse(req.ID, req.Token)
	if _, err := g.session.RequestWithBucketID(http.MethodPost, endpoint, response, endpoint, discordgo.WithContext(ctx)); err != nil {
		g.failed.Add(1)
		slog.Error("Discord Gateway interaction response failed", "interaction_id", req.ID, "error", safeDiscordError(err))
		return
	}
	g.responded.Add(1)
}

func safeDiscordError(err error) error {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) && rest.Response != nil {
		return fmt.Errorf("Discord HTTP status %d", rest.Response.StatusCode)
	}
	var limited *discordgo.RateLimitError
	if errors.As(err, &limited) {
		return fmt.Errorf("Discord HTTP status %d", http.StatusTooManyRequests)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("Discord request deadline exceeded")
	}
	return errors.New("Discord transport request failed")
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
