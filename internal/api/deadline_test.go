package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

func TestGatewayInterruptsStalledHandshake(t *testing.T) {
	for _, stage := range []string{"HELLO", "READY"} {
		for _, interrupt := range []string{"cancel", "timeout"} {
			t.Run(stage+"/"+interrupt, func(t *testing.T) {
				upgrader := websocket.Upgrader{}
				waiting := make(chan *websocket.Conn, 1)
				closed := make(chan struct{})
				mux := http.NewServeMux()
				server := httptest.NewServer(mux)
				defer server.Close()
				mux.HandleFunc("/gateway", func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprintf(w, `{"url":%q}`, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws")
				})
				mux.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					if stage == "READY" {
						_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 60000}})
						if _, _, err := conn.ReadMessage(); err != nil {
							t.Error(err)
							return
						}
					}
					waiting <- conn
					_, _, _ = conn.ReadMessage()
					close(closed)
				})
				oldGateway := discordgo.EndpointGateway
				discordgo.EndpointGateway = server.URL + "/gateway"
				defer func() { discordgo.EndpointGateway = oldGateway }()
				g, err := NewGateway("fixture-token", NewHandler(nil))
				if err != nil {
					t.Fatal(err)
				}
				g.openTimeout = 5 * time.Second
				if interrupt == "timeout" {
					g.openTimeout = 100 * time.Millisecond
				}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { g.Run(ctx); close(done) }()
				var conn *websocket.Conn
				defer func() {
					cancel()
					if conn != nil {
						conn.Close()
					}
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("Gateway did not stop after closing fixture")
					}
				}()
				select {
				case conn = <-waiting:
				case <-time.After(time.Second):
					t.Fatal("fixture did not reach handshake")
				}
				if interrupt == "cancel" {
					cancel()
				}
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatalf("stalled %s socket stayed open after %s", stage, interrupt)
				}
				if interrupt == "cancel" {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("Gateway Run ignored cancellation")
					}
				}
			})
		}
	}
}

func TestGatewayReconnectsAfterHandshakeTimeout(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var connections atomic.Int32
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/gateway", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"url":%q}`, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws")
	})
	mux.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if connections.Add(1) == 1 {
			_, _, _ = conn.ReadMessage() // Withhold HELLO until open timeout.
			return
		}
		_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 60000}})
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		_ = conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{"session_id": "fixture", "user": map[string]string{"id": "123"}}})
		for {
			var packet struct {
				Op int `json:"op"`
			}
			if conn.ReadJSON(&packet) != nil {
				return
			}
			if packet.Op == 1 {
				_ = conn.WriteJSON(map[string]int{"op": 11})
			}
		}
	})
	oldGateway := discordgo.EndpointGateway
	discordgo.EndpointGateway = server.URL + "/gateway"
	defer func() { discordgo.EndpointGateway = oldGateway }()
	g, err := NewGateway("fixture-token", NewHandler(nil))
	if err != nil {
		t.Fatal(err)
	}
	g.openTimeout = 100 * time.Millisecond
	ready := make(chan struct{}, 1)
	g.session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Ready) { ready <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		// DiscordGo deliberately waits one second after its close frame.
		case <-time.After(4 * time.Second):
			t.Error("reconnected Gateway did not stop")
		}
	}()
	select {
	case <-ready:
	case <-time.After(7 * time.Second):
		t.Fatal("Gateway did not reconnect after handshake timeout")
	}
	if connections.Load() != 2 || !g.ready.Load() {
		t.Fatal("Gateway did not become healthy on the replacement connection")
	}
}

func TestDeadlineBoundDiscordRequestsDoNotWaitForRateLimitRetries(t *testing.T) {
	for _, operation := range []string{"application", "register", "interaction"} {
		t.Run(operation, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"retry_after":0.5,"global":false}`)
			}))
			defer server.Close()
			oldAPI, oldApplications := discordgo.EndpointAPI, discordgo.EndpointApplications
			discordgo.EndpointAPI, discordgo.EndpointApplications = server.URL+"/", server.URL+"/applications/"
			defer func() {
				discordgo.EndpointAPI, discordgo.EndpointApplications = oldAPI, oldApplications
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			started := time.Now()
			var err error
			switch operation {
			case "application":
				err = CheckApplication(ctx, "fixture-token", "123")
			case "register":
				err = Register(ctx, "fixture-token", "123", "456")
			case "interaction":
				g, createErr := NewGateway("fixture-token", NewHandler(nil))
				if createErr != nil {
					t.Fatal(createErr)
				}
				deadline, _ := ctx.Deadline()
				g.handleEvent([]byte(`{"id":"123","token":"fixture","type":2}`), deadline)
				if g.failed.Load() != 1 || g.responded.Load() != 0 {
					t.Fatal("rate-limited callback was not recorded as failed")
				}
			}
			if operation != "interaction" && err == nil {
				t.Fatal("rate limit was not surfaced")
			}
			if operation != "interaction" && !strings.Contains(err.Error(), "429") {
				t.Fatalf("rate limit classification was lost: %v", err)
			}
			if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
				t.Fatalf("30ms request waited %s for a rate-limit retry", elapsed)
			}
			if calls.Load() != 1 {
				t.Fatalf("expected one fixture request, got %d", calls.Load())
			}
		})
	}
}

func TestDiscordRateLimitHeadersDoNotBlockLaterRequestPastContext(t *testing.T) {
	for _, scope := range []string{"bucket", "global"} {
		t.Run(scope, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset-After", "0.5")
					if scope == "global" {
						w.Header().Set("X-RateLimit-Global", "true")
					}
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprint(w, `{"retry_after":0.5,"global":false}`)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			s, err := newSession("fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.RequestWithBucketID(http.MethodGet, server.URL, nil, "first"); err == nil {
				t.Fatal("first request did not surface rate limit")
			}
			bucket := "first"
			if scope == "global" {
				bucket = "second"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			started := time.Now()
			_, err = s.RequestWithBucketID(http.MethodGet, server.URL, nil, bucket, discordgo.WithContext(ctx))
			if err != nil {
				t.Fatalf("later request was blocked by cached rate limit: %v", err)
			}
			if elapsed := time.Since(started); elapsed > 200*time.Millisecond || calls.Load() != 2 {
				t.Fatalf("later request elapsed=%s requests=%d", elapsed, calls.Load())
			}
		})
	}
}
