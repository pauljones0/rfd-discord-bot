package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// CheckChannels verifies the application's Gateway configuration and the bot's
// effective access to every subscribed channel. It uses GET requests only; it
// neither connects a Gateway nor registers commands or sends probe messages.
func CheckChannels(ctx context.Context, token, expectedAppID string, subs []models.Subscription) error {
	if strings.TrimSpace(token) == "" || !preflightDiscordID(expectedAppID) {
		return fmt.Errorf("Discord bot token and a valid application ID are required")
	}
	channels := make([]models.Subscription, 0, len(subs))
	seen := make(map[string]string)
	for _, sub := range subs {
		if !sub.IsRFD() || !preflightDiscordID(sub.GuildID) || !preflightDiscordID(sub.ChannelID) {
			return fmt.Errorf("channel preflight requires RFD subscriptions with valid guild and channel IDs")
		}
		if guild, ok := seen[sub.ChannelID]; ok {
			if guild != sub.GuildID {
				return fmt.Errorf("channel %s is assigned to conflicting guilds in subscriptions", sub.ChannelID)
			}
			continue
		}
		seen[sub.ChannelID] = sub.GuildID
		channels = append(channels, sub)
	}
	if err := CheckApplication(ctx, token, expectedAppID); err != nil {
		return fmt.Errorf("verify Discord application: %w", err)
	}
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return fmt.Errorf("could not initialize Discord preflight")
	}
	s.MaxRestRetries = 0
	s.ShouldRetryOnRateLimit = false
	options := discordgo.WithContext(ctx)
	user, err := s.User("@me", options)
	if err != nil {
		return fmt.Errorf("read Discord bot identity: %w", safeDiscordError(err))
	}
	if user == nil || !user.Bot || !preflightDiscordID(user.ID) || user.ID != expectedAppID {
		return fmt.Errorf("Discord bot identity does not match the configured application")
	}
	checkedGuilds := make(map[string]bool)
	for _, sub := range channels {
		channel, err := s.Channel(sub.ChannelID, options)
		if err != nil {
			return fmt.Errorf("read channel %s; ensure this bot is installed and can view it: %w", sub.ChannelID, safeDiscordError(err))
		}
		if channel == nil || channel.ID != sub.ChannelID || channel.GuildID != sub.GuildID {
			return fmt.Errorf("channel %s does not belong to subscribed guild %s", sub.ChannelID, sub.GuildID)
		}
		if channel.Type != discordgo.ChannelTypeGuildText && channel.Type != discordgo.ChannelTypeGuildNews {
			return fmt.Errorf("channel %s must be a text or announcement channel", sub.ChannelID)
		}
		if !checkedGuilds[sub.GuildID] {
			guild, err := s.Guild(sub.GuildID, options)
			if err != nil {
				return fmt.Errorf("read guild %s; ensure this bot is installed: %w", sub.GuildID, safeDiscordError(err))
			}
			if guild == nil || guild.ID != sub.GuildID {
				return fmt.Errorf("Discord returned unexpected guild metadata for %s", sub.GuildID)
			}
			// Decode explicitly: DiscordGo's GuildMember helper dereferences a
			// nil response when an upstream server returns malformed null JSON.
			body, err := s.RequestWithBucketID(http.MethodGet, discordgo.EndpointGuildMember(sub.GuildID, user.ID), nil,
				discordgo.EndpointGuildMember(sub.GuildID, ""), options)
			if err != nil {
				return fmt.Errorf("read bot membership in guild %s; ensure this bot is installed: %w", sub.GuildID, safeDiscordError(err))
			}
			var member discordgo.Member
			if json.Unmarshal(body, &member) != nil || member.User == nil || member.User.ID != user.ID {
				return fmt.Errorf("Discord returned invalid bot membership for guild %s", sub.GuildID)
			}
			if member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(time.Now()) {
				return fmt.Errorf("bot is currently timed out in guild %s", sub.GuildID)
			}
			member.GuildID = sub.GuildID
			guild.Members = []*discordgo.Member{&member}
			if err := s.State.GuildAdd(guild); err != nil {
				return fmt.Errorf("could not prepare guild permission calculation")
			}
			checkedGuilds[sub.GuildID] = true
		}
		if err := s.State.ChannelAdd(channel); err != nil {
			return fmt.Errorf("could not prepare channel permission calculation")
		}
		permissions, err := s.UserChannelPermissions(user.ID, channel.ID, options)
		if err != nil {
			return fmt.Errorf("calculate channel %s permissions: %w", channel.ID, safeDiscordError(err))
		}
		var missing []string
		for _, permission := range []struct {
			bit  int64
			name string
		}{
			{discordgo.PermissionViewChannel, "View Channel"},
			{discordgo.PermissionSendMessages, "Send Messages"},
			{discordgo.PermissionEmbedLinks, "Embed Links"},
		} {
			if permissions&permission.bit == 0 {
				missing = append(missing, permission.name)
			}
		}
		if len(missing) != 0 {
			return fmt.Errorf("bot lacks %s in channel %s", strings.Join(missing, ", "), channel.ID)
		}
	}
	return nil
}

func preflightDiscordID(value string) bool {
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == value
}
