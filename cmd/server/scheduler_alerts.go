package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const scheduledProcessorIssueRepeatInterval = 30 * time.Minute

type scheduledProcessorFailure struct {
	FirstFailedAt time.Time
	LastFailedAt  time.Time
	LastAlertAt   time.Time
	Signature     string
	AlertSent     bool
}

func (s *Server) reportScheduledProcessorFailure(processorName string, timeout, duration time.Duration, err error) {
	if s == nil || err == nil || errors.Is(err, context.Canceled) {
		return
	}
	if !scheduledProcessorAlertEnabled(processorName) {
		return
	}

	now := time.Now()
	signature := scheduledFailureSignature(err)
	state, shouldSend := s.recordScheduledProcessorFailure(processorName, now, signature)
	if !shouldSend {
		return
	}

	label := scheduledProcessorLabel(processorName)
	fields := []models.CoreSystemAlertField{
		{Name: "Automatic handling", Value: "The current run was stopped or failed. The scheduler will retry on the next poll."},
		{Name: "Alert suppression", Value: fmt.Sprintf("Matching %s failures are suppressed for %s unless the error changes.", label, scheduledProcessorIssueRepeatInterval.String())},
		{Name: "First failed", Value: state.FirstFailedAt.UTC().Format(time.RFC3339)},
		{Name: "Duration", Value: duration.Round(time.Millisecond).String()},
		{Name: "Timeout", Value: timeout.String()},
	}
	if strings.Contains(strings.ToLower(signature), "context deadline exceeded") {
		fields = append(fields, models.CoreSystemAlertField{
			Name:  "Likely cause",
			Value: "The processor or one of its dependencies exceeded the job context deadline. This often points at upstream latency, database pressure, or a run that kept working after cancellation.",
		})
	}

	sent := s.sendScheduledProcessorAlert(processorName, models.CoreSystemAlert{
		Title:      label + " monitor failure",
		Severity:   "error",
		Component:  processorName + "-scheduler",
		Details:    fmt.Sprintf("Processor %q failed: %s", processorName, err.Error()),
		OccurredAt: now,
		Fields:     fields,
	})
	if sent {
		s.markScheduledProcessorFailureAlertSent(processorName, signature, now)
	}
}

func (s *Server) reportScheduledProcessorRecovery(processorName string, duration time.Duration) {
	if s == nil || !scheduledProcessorAlertEnabled(processorName) {
		return
	}
	now := time.Now()
	state, hadAlertedFailure := s.clearScheduledProcessorFailure(processorName)
	if !hadAlertedFailure {
		return
	}

	label := scheduledProcessorLabel(processorName)
	s.sendScheduledProcessorAlert(processorName, models.CoreSystemAlert{
		Title:      label + " monitor recovered",
		Severity:   "info",
		Component:  processorName + "-scheduler",
		Details:    fmt.Sprintf("Processor %q completed successfully after a previous failure.", processorName),
		OccurredAt: now,
		Fields: []models.CoreSystemAlertField{
			{Name: "First failed", Value: state.FirstFailedAt.UTC().Format(time.RFC3339)},
			{Name: "Last failed", Value: state.LastFailedAt.UTC().Format(time.RFC3339)},
			{Name: "Recovery duration", Value: duration.Round(time.Millisecond).String()},
		},
	})
}

func (s *Server) recordScheduledProcessorFailure(processorName string, now time.Time, signature string) (scheduledProcessorFailure, bool) {
	s.schedulerIssueMu.Lock()
	defer s.schedulerIssueMu.Unlock()

	if s.schedulerFailures == nil {
		s.schedulerFailures = make(map[string]scheduledProcessorFailure)
	}
	state, ok := s.schedulerFailures[processorName]
	if !ok || state.Signature != signature {
		state = scheduledProcessorFailure{
			FirstFailedAt: now,
			Signature:     signature,
		}
	}
	state.LastFailedAt = now

	shouldSend := !state.AlertSent || now.Sub(state.LastAlertAt) >= scheduledProcessorIssueRepeatInterval
	s.schedulerFailures[processorName] = state
	return state, shouldSend
}

func (s *Server) markScheduledProcessorFailureAlertSent(processorName, signature string, sentAt time.Time) {
	s.schedulerIssueMu.Lock()
	defer s.schedulerIssueMu.Unlock()

	if s.schedulerFailures == nil {
		return
	}
	state, ok := s.schedulerFailures[processorName]
	if !ok || state.Signature != signature {
		return
	}
	state.LastAlertAt = sentAt
	state.AlertSent = true
	s.schedulerFailures[processorName] = state
}

func (s *Server) clearScheduledProcessorFailure(processorName string) (scheduledProcessorFailure, bool) {
	s.schedulerIssueMu.Lock()
	defer s.schedulerIssueMu.Unlock()

	if s.schedulerFailures == nil {
		return scheduledProcessorFailure{}, false
	}
	state, ok := s.schedulerFailures[processorName]
	if !ok {
		return scheduledProcessorFailure{}, false
	}
	delete(s.schedulerFailures, processorName)
	return state, state.AlertSent
}

func (s *Server) sendScheduledProcessorAlert(processorName string, alert models.CoreSystemAlert) bool {
	if s == nil || s.store == nil || s.systemNotifier == nil {
		slog.Error("Scheduled processor alert dependencies missing",
			"processor", processorName,
			"title", alert.Title,
			"server_nil", s == nil,
			"store_nil", s == nil || s.store == nil,
			"notifier_nil", s == nil || s.systemNotifier == nil,
		)
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	subs, err := s.store.GetAllSubscriptions(ctx)
	if err != nil {
		slog.Error("Failed to load subscriptions for scheduled processor alert", "processor", processorName, "title", alert.Title, "error", err)
		return false
	}
	filtered := scheduledProcessorSubscriptions(processorName, subs)
	if len(filtered) == 0 {
		slog.Info("No subscriptions for scheduled processor alert", "processor", processorName, "title", alert.Title)
		return false
	}
	if err := s.systemNotifier.SendCoreSystemAlert(ctx, alert, filtered); err != nil {
		slog.Error("Failed to send scheduled processor alert", "processor", processorName, "title", alert.Title, "error", err)
		return false
	}
	return true
}

func scheduledProcessorSubscriptions(processorName string, subs []models.Subscription) []models.Subscription {
	out := make([]models.Subscription, 0, len(subs))
	for _, sub := range subs {
		switch processorName {
		case "rfd":
			if sub.IsRFD() {
				out = append(out, sub)
			}
		case "ebay":
			if sub.IsEbay() {
				out = append(out, sub)
			}
		case "memoryexpress":
			if sub.IsMemoryExpress() {
				out = append(out, sub)
			}
		case "bestbuy":
			if sub.IsBestBuy() && sub.DealType != dealtypes.BestBuyCompute {
				out = append(out, sub)
			}
		case "bestbuy_compute":
			if sub.IsBestBuy() && sub.DealType == dealtypes.BestBuyCompute {
				out = append(out, sub)
			}
		}
	}
	return dedupeSubscriptionsByChannel(out)
}

func dedupeSubscriptionsByChannel(subs []models.Subscription) []models.Subscription {
	out := make([]models.Subscription, 0, len(subs))
	seen := make(map[string]struct{}, len(subs))
	for _, sub := range subs {
		channelID := strings.TrimSpace(sub.ChannelID)
		if channelID == "" {
			continue
		}
		if _, ok := seen[channelID]; ok {
			continue
		}
		seen[channelID] = struct{}{}
		out = append(out, sub)
	}
	return out
}

func scheduledProcessorAlertEnabled(processorName string) bool {
	switch processorName {
	case "rfd", "ebay", "memoryexpress", "bestbuy", "bestbuy_compute":
		return true
	default:
		return false
	}
}

func scheduledProcessorLabel(processorName string) string {
	switch processorName {
	case "rfd":
		return "RFD"
	case "ebay":
		return "eBay"
	case "memoryexpress":
		return "Memory Express"
	case "bestbuy":
		return "Best Buy"
	case "bestbuy_compute":
		return "Best Buy compute"
	default:
		return processorName
	}
}

func scheduledFailureSignature(err error) string {
	if err == nil {
		return ""
	}
	signature := strings.TrimSpace(err.Error())
	if signature == "" {
		return fmt.Sprintf("%T", err)
	}
	return signature
}
