package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandsEnforceGuildPermissionsAndPersistSubscriptions(t *testing.T) {
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "rfd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := NewHandler(store)
	invoke := func(permissions, subcommand, options string) string {
		t.Helper()
		body := fmt.Sprintf(`{"type":2,"guild_id":"guild","member":{"permissions":%q,"user":{"id":"user"}},"data":{"name":"rfd","options":[{"name":%q,"options":%s}],"resolved":{"channels":{"123":{"name":"deals","type":0}}}}}`, permissions, subcommand, options)
		var req interactionRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		result := privateReply(h.handleInteraction(context.Background(), req))
		if result.Data.Flags != 64 {
			t.Fatal("management response was not private")
		}
		return result.Data.Content
	}
	options := `[{"name":"channel","value":"123"},{"name":"filter","value":"rfd_hot"}]`
	if got := invoke("0", "subscribe", options); !strings.Contains(got, "Manage Server") {
		t.Fatalf("non-admin allowed: %s", got)
	}
	rows, _ := store.GetSubscriptionsByGuild(context.Background(), "guild")
	if len(rows) != 0 {
		t.Fatal("non-admin mutated subscriptions")
	}
	if got := invoke("32", "subscribe", options); !strings.Contains(got, "enabled") {
		t.Fatal(got)
	}
	if got := invoke("32", "list", `[]`); !strings.Contains(got, "<#123>") {
		t.Fatal(got)
	}
	if got := invoke("32", "unsubscribe", options); !strings.Contains(got, "Removed") {
		t.Fatal(got)
	}
	rows, _ = store.GetSubscriptionsByGuild(context.Background(), "guild")
	if len(rows) != 0 {
		t.Fatal("unsubscribe did not persist")
	}
}
