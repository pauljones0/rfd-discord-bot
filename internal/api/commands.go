package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type SubscriptionStore interface {
	SaveSubscription(context.Context, models.Subscription) error
	RemoveSubscription(context.Context, string, string, string) error
	GetSubscriptionsByGuild(context.Context, string) ([]models.Subscription, error)
}
type Handler struct{ store SubscriptionStore }

func NewHandler(store SubscriptionStore) *Handler { return &Handler{store: store} }
func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("Could not encode command response", "error", err)
	}
}
func reply(w http.ResponseWriter, content string) {
	writeJSON(w, map[string]any{"type": 4, "data": map[string]any{"content": content, "flags": 64, "allowed_mentions": map[string]any{"parse": []string{}}}})
}

type interactionRequest struct {
	Type    int    `json:"type"`
	GuildID string `json:"guild_id"`
	Member  *struct {
		Permissions string `json:"permissions"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"member"`
	Data struct {
		Name     string   `json:"name"`
		Options  []option `json:"options"`
		Resolved struct {
			Channels map[string]struct {
				Name string `json:"name"`
				Type int    `json:"type"`
			}
		} `json:"resolved"`
	} `json:"data"`
}
type option struct {
	Name    string   `json:"name"`
	Value   any      `json:"value"`
	Options []option `json:"options"`
}

func optionValue(options []option, name string) string {
	for _, o := range options {
		if o.Name == name {
			v, _ := o.Value.(string)
			return v
		}
	}
	return ""
}

func (h *Handler) handleInteraction(w http.ResponseWriter, body []byte) {
	var req interactionRequest
	if json.Unmarshal(body, &req) != nil {
		reply(w, "Could not read this command.")
		return
	}
	if req.Type != 2 || req.Data.Name != "rfd" {
		reply(w, "Use an /rfd command.")
		return
	}
	if req.GuildID == "" || req.Member == nil {
		reply(w, "Use this command in a Discord server.")
		return
	}
	permissions, err := strconv.ParseUint(req.Member.Permissions, 10, 64)
	if err != nil || permissions&(discordgo.PermissionManageServer|discordgo.PermissionAdministrator) == 0 {
		reply(w, "You need Manage Server permission to manage RFD alerts.")
		return
	}
	if len(req.Data.Options) != 1 {
		reply(w, "Choose subscribe, unsubscribe, or list.")
		return
	}
	subcommand := req.Data.Options[0]
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	switch subcommand.Name {
	case "list":
		subs, err := h.store.GetSubscriptionsByGuild(ctx, req.GuildID)
		if err != nil {
			reply(w, "Could not read subscriptions. Please try again.")
			return
		}
		if len(subs) == 0 {
			reply(w, "No RFD subscriptions yet. Use /rfd subscribe to add one.")
			return
		}
		var out strings.Builder
		for i, s := range subs {
			line := fmt.Sprintf("<#%s> — %s\n", s.ChannelID, dealtypes.Label(s.DealType))
			if out.Len()+len(line) > 1800 {
				fmt.Fprintf(&out, "…and %d more subscriptions.", len(subs)-i)
				break
			}
			out.WriteString(line)
		}
		reply(w, out.String())
	case "subscribe", "unsubscribe":
		channel := optionValue(subcommand.Options, "channel")
		filter := optionValue(subcommand.Options, "filter")
		resolved, ok := req.Data.Resolved.Channels[channel]
		if channel == "" || !ok || (resolved.Type != 0 && resolved.Type != 5) {
			reply(w, "Select a text or announcement channel in this server.")
			return
		}
		if subcommand.Name == "subscribe" {
			if !dealtypes.IsRFD(filter) {
				reply(w, "Select one of the available RFD filters.")
				return
			}
			err = h.store.SaveSubscription(ctx, models.Subscription{GuildID: req.GuildID, ChannelID: channel, ChannelName: resolved.Name, DealType: filter, SubscriptionType: "rfd", AddedBy: req.Member.User.ID, AddedAt: time.Now()})
			if err != nil {
				reply(w, "Could not save this subscription. Please try again.")
				return
			}
			reply(w, fmt.Sprintf("RFD alerts enabled in <#%s>: %s.", channel, dealtypes.Label(filter)))
		} else {
			if filter != "" && !dealtypes.IsRFD(filter) {
				reply(w, "Select a valid RFD filter, or omit it to remove all RFD filters for this channel.")
				return
			}
			err = h.store.RemoveSubscription(ctx, req.GuildID, channel, filter)
			if err != nil {
				reply(w, "Could not remove this subscription. Please try again.")
				return
			}
			reply(w, fmt.Sprintf("Removed the selected RFD subscription(s) from <#%s>.", channel))
		}
	default:
		reply(w, "Unknown RFD subcommand.")
	}
}

// Command returns a single guild-only command with explicit management rights.
// Registration upserts this command; it never bulk-deletes unrelated commands.
func Command() *discordgo.ApplicationCommand {
	permissions := int64(discordgo.PermissionManageServer)
	dm := false
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(dealtypes.RFDChoices))
	for _, c := range dealtypes.RFDChoices {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: c.Name, Value: c.Value})
	}
	channel := func() *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Channel for RFD alerts", Required: true, ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews}}
	}
	filter := func(required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "filter", Description: "Which deals to include", Required: required, Choices: choices}
	}
	return &discordgo.ApplicationCommand{Name: "rfd", Description: "Manage RedFlagDeals alerts", DefaultMemberPermissions: &permissions, DMPermission: &dm, Options: []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "subscribe", Description: "Enable RFD alerts in a channel", Options: []*discordgo.ApplicationCommandOption{channel(), filter(true)}},
		{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "unsubscribe", Description: "Remove RFD alerts from a channel", Options: []*discordgo.ApplicationCommandOption{channel(), filter(false)}},
		{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "list", Description: "List this server's RFD subscriptions"},
	}}
}

func Register(ctx context.Context, token, appID, guildID string) error {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return err
	}
	_, err = s.ApplicationCommandCreate(appID, guildID, Command(), discordgo.WithContext(ctx))
	return safeDiscordErrorOrNil(err)
}
func safeDiscordErrorOrNil(err error) error {
	if err == nil {
		return nil
	}
	return safeDiscordError(err)
}

// CheckApplication catches the common configuration that silently routes all
// interactions to an old webhook instead of this outbound Gateway connection.
func CheckApplication(ctx context.Context, token, expectedID string) error {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return err
	}
	body, err := s.RequestWithBucketID(http.MethodGet, discordgo.EndpointAPI+"applications/@me", nil, "applications/@me", discordgo.WithContext(ctx))
	if err != nil {
		return safeDiscordError(err)
	}
	var app struct {
		ID       string `json:"id"`
		Endpoint string `json:"interactions_endpoint_url"`
	}
	if err = json.Unmarshal(body, &app); err != nil {
		return fmt.Errorf("could not read Discord application settings")
	}
	if app.ID != expectedID {
		return fmt.Errorf("DISCORD_APP_ID does not match this bot token")
	}
	if app.Endpoint != "" {
		return fmt.Errorf("clear the Interactions Endpoint URL in the Discord Developer Portal to enable Gateway delivery")
	}
	return nil
}
