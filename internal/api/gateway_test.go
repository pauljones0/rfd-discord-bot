package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type interactionResponse struct {
	Type int `json:"type"`
	Data *struct {
		Content string `json:"content"`
		Flags   int    `json:"flags"`
	} `json:"data"`
}

func TestGatewayReceivesCommandAndResumesAfterDisconnect(t *testing.T) {
	// Exercise actual DiscordGo WebSocket and REST transport against local
	// servers. No Discord credentials, messages, or production database used.
	var connections atomic.Int32
	callbacks := make(chan interactionResponse, 1)
	resumed := make(chan struct{}, 1)
	serverErrors := make(chan error, 4)
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	defer ts.Close()
	mux.HandleFunc("/gateway", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"url": "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"})
	})
	mux.HandleFunc("/interactions/123/token/callback", func(w http.ResponseWriter, r *http.Request) {
		var response interactionResponse
		if err := json.NewDecoder(r.Body).Decode(&response); err != nil {
			serverErrors <- err
		}
		callbacks <- response
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
		_ = c.WriteJSON(map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 60000}})
		var identify struct {
			Op int `json:"op"`
			D  struct {
				Intents int `json:"intents"`
			} `json:"d"`
		}
		if err := c.ReadJSON(&identify); err != nil {
			serverErrors <- err
			return
		}
		if connections.Add(1) == 1 {
			if identify.Op != 2 || identify.D.Intents != 0 {
				serverErrors <- errors.New("unexpected Identify or privileged intents")
			}
			_ = c.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{"session_id": "local-session", "user": map[string]string{"id": "bot"}}})
			_ = c.WriteJSON(map[string]any{"op": 0, "s": 2, "t": "INTERACTION_CREATE", "d": json.RawMessage(`{"id":"123","token":"token","application_id":"bot","type":2,"guild_id":"guild","member":{"permissions":"32","user":{"id":"user"}},"data":{"name":"rfd","options":[{"name":"list","type":1}]}}`)})
			_ = c.WriteJSON(map[string]int{"op": 7})
		} else {
			if identify.Op != 6 {
				serverErrors <- errors.New("expected session Resume after disconnection")
			}
			_ = c.WriteJSON(map[string]any{"op": 0, "s": 3, "t": "RESUMED", "d": map[string]any{}})
			resumed <- struct{}{}
		}
		for {
			var msg struct {
				Op int `json:"op"`
			}
			if c.ReadJSON(&msg) != nil {
				return
			}
			if msg.Op == 1 {
				_ = c.WriteJSON(map[string]int{"op": 11})
			}
		}
	})
	oldGateway, oldAPI := discordgo.EndpointGateway, discordgo.EndpointAPI
	discordgo.EndpointGateway, discordgo.EndpointAPI = ts.URL+"/gateway", ts.URL+"/"
	defer func() { discordgo.EndpointGateway, discordgo.EndpointAPI = oldGateway, oldAPI }()
	store, err := storage.Open(context.Background(), t.TempDir()+"/rfd.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	g, err := NewGateway("local-test-token", NewHandler(store))
	if err != nil {
		t.Fatal(err)
	}
	g.openTimeout = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("Gateway did not stop")
		}
	}()
	select {
	case response := <-callbacks:
		if response.Type != 4 || response.Data == nil || response.Data.Flags != 64 || !strings.Contains(response.Data.Content, "No RFD subscriptions") {
			t.Fatalf("unexpected command response: %+v", response)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-time.After(4 * time.Second):
		t.Fatal("no REST callback after Gateway interaction")
	}
	select {
	case <-resumed:
	case err := <-serverErrors:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("Gateway did not reconnect and resume")
	}
	// A completed READY/RESUMED handshake must disarm the open timeout; the
	// same live connection remains usable until runtime cancellation below.
	select {
	case err := <-serverErrors:
		t.Fatal(err)
	case <-time.After(150 * time.Millisecond):
	}
	if !g.ready.Load() || connections.Load() != 2 {
		t.Fatal("healthy resumed socket was closed by handshake timeout")
	}
	if g.received.Load() != 1 || g.responded.Load() != 1 || g.failed.Load() != 0 {
		t.Fatalf("unexpected counters: received=%d responded=%d failed=%d", g.received.Load(), g.responded.Load(), g.failed.Load())
	}
}
