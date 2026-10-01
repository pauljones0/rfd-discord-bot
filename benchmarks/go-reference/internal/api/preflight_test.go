package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type preflightFixture struct {
	appID    string
	endpoint string
	user     *discordgo.User
	channel  *discordgo.Channel
	guild    *discordgo.Guild
	member   any
	status   map[string]int
	mu       sync.Mutex
	requests map[string]int
}

func newPreflightFixture(t *testing.T) *preflightFixture {
	t.Helper()
	f := &preflightFixture{
		appID: "222", user: &discordgo.User{ID: "222", Bot: true},
		channel: &discordgo.Channel{ID: "456", GuildID: "123", Type: discordgo.ChannelTypeGuildText},
		guild: &discordgo.Guild{ID: "123", OwnerID: "789", Roles: []*discordgo.Role{
			{ID: "123", Permissions: discordgo.PermissionViewChannel},
			{ID: "777", Permissions: discordgo.PermissionSendMessages | discordgo.PermissionEmbedLinks},
		}},
		member: &discordgo.Member{User: &discordgo.User{ID: "222", Bot: true}, Roles: []string{"777"}},
		status: map[string]int{}, requests: map[string]int{},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests[r.Method+" "+r.URL.Path]++
		f.mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("preflight attempted a mutation: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected mutation", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bot fixture-secret-token" {
			t.Error("missing fixture bot authorization")
		}
		if code := f.status[r.URL.Path]; code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"message":"fixture-secret-token https://token@example.test/private","code":50001}`))
			return
		}
		var response any
		switch r.URL.Path {
		case "/applications/@me":
			response = map[string]any{"id": f.appID, "interactions_endpoint_url": f.endpoint}
		case "/users/@me":
			response = f.user
		case "/channels/456":
			response = f.channel
		case "/guilds/123":
			response = f.guild
		case "/guilds/123/members/222":
			response = f.member
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	api, users, channels, guilds := discordgo.EndpointAPI, discordgo.EndpointUsers, discordgo.EndpointChannels, discordgo.EndpointGuilds
	discordgo.EndpointAPI, discordgo.EndpointUsers = ts.URL+"/", ts.URL+"/users/"
	discordgo.EndpointChannels, discordgo.EndpointGuilds = ts.URL+"/channels/", ts.URL+"/guilds/"
	t.Cleanup(func() {
		ts.Close()
		discordgo.EndpointAPI, discordgo.EndpointUsers = api, users
		discordgo.EndpointChannels, discordgo.EndpointGuilds = channels, guilds
	})
	return f
}

func TestPreflightChecksEffectivePermissionsWithOnlyGETsAndDeduplicatesChannels(t *testing.T) {
	f := newPreflightFixture(t)
	f.channel.Type = discordgo.ChannelTypeGuildNews
	subs := []models.Subscription{
		{GuildID: "123", ChannelID: "456", DealType: "rfd_hot"},
		{GuildID: "123", ChannelID: "456", DealType: "rfd_tech"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := CheckChannels(ctx, "fixture-secret-token", "222", subs); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 5 {
		t.Fatalf("expected five distinct GET endpoints, got %#v", f.requests)
	}
	for endpoint, count := range f.requests {
		if !strings.HasPrefix(endpoint, "GET ") || count != 1 {
			t.Fatalf("unexpected request count or mutation: %s = %d", endpoint, count)
		}
	}
}

func TestPreflightHonorsMemberOverwriteAfterRoleDeny(t *testing.T) {
	f := newPreflightFixture(t)
	f.channel.PermissionOverwrites = []*discordgo.PermissionOverwrite{
		{ID: "777", Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionEmbedLinks},
		{ID: "222", Type: discordgo.PermissionOverwriteTypeMember, Allow: discordgo.PermissionEmbedLinks},
	}
	if err := CheckChannels(context.Background(), "fixture-secret-token", "222", []models.Subscription{{GuildID: "123", ChannelID: "456"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightRejectsInaccessibleOrInvalidDiscordConfiguration(t *testing.T) {
	tests := []struct {
		name string
		edit func(*preflightFixture)
		want string
	}{
		{"wrong application", func(f *preflightFixture) { f.appID = "999" }, "does not match"},
		{"old webhook", func(f *preflightFixture) { f.endpoint = "https://example.test/old" }, "Interactions Endpoint"},
		{"not a bot", func(f *preflightFixture) { f.user.Bot = false }, "identity does not match"},
		{"wrong bot identity", func(f *preflightFixture) { f.user.ID = "999" }, "identity does not match"},
		{"wrong guild", func(f *preflightFixture) { f.channel.GuildID = "999" }, "does not belong"},
		{"voice channel", func(f *preflightFixture) { f.channel.Type = discordgo.ChannelTypeGuildVoice }, "text or announcement"},
		{"bot not installed", func(f *preflightFixture) { f.status["/guilds/123"] = 403 }, "ensure this bot is installed"},
		{"membership missing", func(f *preflightFixture) { f.status["/guilds/123/members/222"] = 404 }, "ensure this bot is installed"},
		{"channel inaccessible", func(f *preflightFixture) { f.status["/channels/456"] = 403 }, "can view it"},
		{"missing embed role permission", func(f *preflightFixture) { f.guild.Roles[1].Permissions = discordgo.PermissionSendMessages }, "Embed Links"},
		{"member deny beats role allow", func(f *preflightFixture) {
			f.channel.PermissionOverwrites = []*discordgo.PermissionOverwrite{{ID: "222", Type: discordgo.PermissionOverwriteTypeMember, Deny: discordgo.PermissionSendMessages}}
		}, "Send Messages"},
		{"everyone deny", func(f *preflightFixture) {
			f.channel.PermissionOverwrites = []*discordgo.PermissionOverwrite{{ID: "123", Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionViewChannel}}
		}, "View Channel"},
		{"null member", func(f *preflightFixture) { f.member = nil }, "invalid bot membership"},
		{"timed out member", func(f *preflightFixture) {
			until := time.Now().Add(time.Hour)
			f.member.(*discordgo.Member).CommunicationDisabledUntil = &until
		}, "timed out"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newPreflightFixture(t)
			test.edit(f)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := CheckChannels(ctx, "fixture-secret-token", "222", []models.Subscription{{GuildID: "123", ChannelID: "456"}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wanted %q, got %v", test.want, err)
			}
			if strings.Contains(err.Error(), "fixture-secret-token") || strings.Contains(err.Error(), "https://token@") {
				t.Fatalf("preflight exposed credentials: %v", err)
			}
		})
	}
}

func TestPreflightRejectsConflictingSubscriptionsBeforeRequests(t *testing.T) {
	f := newPreflightFixture(t)
	err := CheckChannels(context.Background(), "fixture-secret-token", "222", []models.Subscription{
		{GuildID: "123", ChannelID: "456"}, {GuildID: "999", ChannelID: "456"},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicting guilds") {
		t.Fatalf("wanted conflicting guild error, got %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Fatal("invalid local input made Discord requests")
	}
}
