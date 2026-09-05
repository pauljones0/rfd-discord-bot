// Package ai provides optional Gemini title cleanup. It does not select deals.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
	"google.golang.org/genai"
)

var ErrQuotaCooldown = errors.New("Gemini quota cooldown active")

const exhaustionCooldown = 30 * time.Minute

type QuotaStore interface {
	GetGeminiQuotaStatus(context.Context) (*models.GeminiQuotaStatus, error)
	UpdateGeminiQuotaStatus(context.Context, models.GeminiQuotaStatus) error
}

// Client visits configured models in order for each API key. Quota state uses
// the existing storage format so a restart or upgrade keeps an active cooldown.
type Client struct {
	mu                   sync.Mutex
	clients              []*genai.Client
	modelIDs             []string
	store                QuotaStore
	state                models.GeminiQuotaStatus
	keyIndex, modelIndex int
	rateLimits, timeouts int
	usage                usage
}

type usage struct {
	requests, inputTokens, outputTokens, parseFailures, retries int
}

type CleanTitleResult struct {
	Index      int    `json:"index"`
	CleanTitle string `json:"clean_title"`
}

func NewClient(ctx context.Context, apiKeys, modelIDs []string, store QuotaStore) (*Client, error) {
	if len(apiKeys) == 0 {
		return nil, nil
	}
	if len(modelIDs) == 0 {
		return nil, fmt.Errorf("Gemini title cleanup requires a model")
	}
	c := &Client{modelIDs: append([]string(nil), modelIDs...), store: store}
	for _, key := range apiKeys {
		client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI})
		if err != nil {
			return nil, fmt.Errorf("create Gemini client: %w", err)
		}
		c.clients = append(c.clients, client)
	}
	if store != nil {
		state, err := store.GetGeminiQuotaStatus(ctx)
		if err != nil {
			slog.Warn("Cannot load Gemini quota state; using first configured model", "error", err)
		} else if state != nil {
			c.state = *state
		}
	}
	c.restoreState(ctx)
	return c, nil
}

func getPacificDate() string {
	zone, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		zone = time.FixedZone("PST", -8*60*60)
	}
	return time.Now().In(zone).Format("2006-01-02")
}

func (c *Client) restoreState(ctx context.Context) {
	for i, model := range c.modelIDs {
		if model == c.state.CurrentModel {
			c.modelIndex = i
			break
		}
	}
	for i := range c.clients {
		if c.state.CurrentLocation == fmt.Sprintf("key%d", i) {
			c.keyIndex = i
			break
		}
	}
	if c.state.CurrentDay != getPacificDate() || c.cooldownExpired() {
		c.reset(ctx)
	} else {
		// Normalize removed models/keys without bypassing an active cooldown.
		c.save(ctx)
	}
}

func (c *Client) cooldownExpired() bool {
	return c.state.AllExhausted && !c.state.ExhaustedAt.IsZero() && time.Since(c.state.ExhaustedAt) >= exhaustionCooldown
}

func (c *Client) reset(ctx context.Context) {
	c.keyIndex, c.modelIndex, c.rateLimits, c.timeouts = 0, 0, 0, 0
	c.state = models.GeminiQuotaStatus{CurrentDay: getPacificDate()}
	c.save(ctx)
}

// All state helpers are called under mu after construction. No network request
// holds mu; every retry checks shared cooldown again before contacting Gemini.
func (c *Client) save(ctx context.Context) {
	c.state.CurrentModel = c.modelIDs[c.modelIndex]
	c.state.CurrentLocation = fmt.Sprintf("key%d", c.keyIndex)
	c.state.LastUpdated = time.Now()
	if c.store != nil {
		if err := c.store.UpdateGeminiQuotaStatus(ctx, c.state); err != nil {
			slog.Warn("Cannot save Gemini quota state", "error", err)
		}
	}
}

func (c *Client) nextKey(ctx context.Context) bool {
	if c.keyIndex+1 == len(c.clients) {
		return false
	}
	c.keyIndex++
	c.modelIndex, c.rateLimits, c.timeouts = 0, 0, 0
	c.save(ctx)
	return true
}

func (c *Client) nextModel(ctx context.Context) error {
	if c.modelIndex+1 < len(c.modelIDs) {
		c.modelIndex++
		c.save(ctx)
		return nil
	}
	if c.nextKey(ctx) {
		return nil
	}
	c.state.AllExhausted = true
	c.state.ExhaustedAt = time.Now()
	c.save(ctx)
	return util.PermanentError(ErrQuotaCooldown)
}

func (c *Client) generationClient(ctx context.Context) (*genai.Client, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if c.state.CurrentDay != getPacificDate() || c.cooldownExpired() {
		c.reset(ctx)
	}
	if c.state.AllExhausted {
		return nil, "", util.PermanentError(ErrQuotaCooldown)
	}
	return c.clients[c.keyIndex], c.modelIDs[c.modelIndex], nil
}

func (c *Client) handleError(ctx context.Context, err error) (error, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return util.PermanentError(ctx.Err()), 0
	}
	var apiErr genai.APIError
	code := 0
	if errors.As(err, &apiErr) {
		code = apiErr.Code
	}
	message := err.Error()
	switch {
	case code == 429 || code == 404:
		c.rateLimits++
		if c.rateLimits < 3 {
			return err, 5 * time.Second
		}
		c.rateLimits = 0
		if nextErr := c.nextModel(ctx); nextErr != nil {
			return nextErr, 0
		}
		return err, 0
	case code == 400 && (strings.Contains(message, "is not supported") || strings.Contains(message, "not available")):
		if nextErr := c.nextModel(ctx); nextErr != nil {
			return nextErr, 0
		}
		return err, 0
	case code == 504 || errors.Is(err, context.DeadlineExceeded):
		c.timeouts++
		if c.timeouts >= 5 {
			c.timeouts = 0
			c.nextKey(ctx)
		}
		return err, 0
	case code >= 500:
		return err, 0
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return err, 0
	}
	return util.PermanentError(err), 0
}

// DrainUsage reports actual generation attempts, including repair/retry calls.
// A skipped batch contributes no requests, tokens, or parsing failures.
func (c *Client) DrainUsage() (requests, inputTokens, outputTokens, parseFailures, retries int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	u := c.usage
	c.usage = usage{}
	return u.requests, u.inputTokens, u.outputTokens, u.parseFailures, u.retries
}

// DrainTokens retains the original processor interface while callers migrate to
// DrainUsage for accurate request and failure counts.
func (c *Client) DrainTokens() (int, int) {
	_, input, output, _, _ := c.DrainUsage()
	return input, output
}

func (c *Client) CleanTitles(ctx context.Context, requests []models.TitleRequest) (map[int]string, error) {
	if c == nil || len(requests) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	results := make(map[int]string, len(requests))
	pending := append([]models.TitleRequest(nil), requests...)
	for pass := 0; pass < 2 && len(pending) != 0; pass++ {
		extracted, err := c.generate(ctx, buildCleanTitlesPrompt(pending, pass > 0))
		if err != nil {
			if len(results) == 0 {
				return nil, err
			}
			slog.Warn("Gemini repair failed; retaining completed titles", "completed", len(results), "error", err)
			break
		}
		expected := make(map[int]bool, len(pending))
		for _, request := range pending {
			expected[request.Index] = true
		}
		for _, result := range extracted {
			if title := strings.TrimSpace(result.CleanTitle); expected[result.Index] && title != "" {
				results[result.Index] = title
			}
		}
		missing := make([]models.TitleRequest, 0, len(pending))
		for _, request := range pending {
			if results[request.Index] == "" {
				missing = append(missing, request)
			}
		}
		pending = missing
	}
	return results, nil
}

func (c *Client) generate(ctx context.Context, prompt string) ([]CleanTitleResult, error) {
	var results []CleanTitleResult
	err := util.RetryWithBackoff(ctx, 3, func(attempt int) error {
		client, model, err := c.generationClient(ctx)
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.usage.requests++
		if attempt > 0 {
			c.usage.retries++
		}
		c.mu.Unlock()
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		response, err := client.Models.GenerateContent(callCtx, model, genai.Text(prompt), &genai.GenerateContentConfig{
			Temperature: genai.Ptr[float32](0.1), ResponseMIMEType: "application/json",
		})
		if err != nil {
			err, delay := c.handleError(ctx, err)
			if delay > 0 && attempt < 3 {
				select {
				case <-ctx.Done():
					return util.PermanentError(ctx.Err())
				case <-time.After(delay):
				}
			}
			return err
		}
		c.mu.Lock()
		c.rateLimits, c.timeouts = 0, 0
		if response != nil && response.UsageMetadata != nil {
			c.usage.inputTokens += int(response.UsageMetadata.PromptTokenCount)
			c.usage.outputTokens += int(response.UsageMetadata.CandidatesTokenCount)
		}
		c.mu.Unlock()
		results, err = parseTitles(response)
		if err != nil {
			c.mu.Lock()
			c.usage.parseFailures++
			c.mu.Unlock()
		}
		return err
	})
	return results, err
}

func parseTitles(response *genai.GenerateContentResponse) ([]CleanTitleResult, error) {
	if response == nil || len(response.Candidates) == 0 || response.Candidates[0] == nil || response.Candidates[0].Content == nil {
		return nil, fmt.Errorf("Gemini returned no title content")
	}
	var all strings.Builder
	for _, part := range response.Candidates[0].Content.Parts {
		if part == nil || part.Text == "" || part.Thought {
			continue
		}
		var titles []CleanTitleResult
		if err := json.Unmarshal([]byte(stripCodeBlock(part.Text)), &titles); err == nil {
			return titles, nil
		}
		all.WriteString(part.Text)
	}
	var titles []CleanTitleResult
	if err := json.Unmarshal([]byte(stripCodeBlock(all.String())), &titles); err != nil {
		return nil, fmt.Errorf("parse Gemini title response: %w", err)
	}
	return titles, nil
}

func stripCodeBlock(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	s = extractJSONValue(s)
	return s
}

// extractJSONValue finds the outermost JSON object or array in s by tracking
// brace/bracket depth, returning only that portion. If no valid boundary is
// found the original string is returned unchanged so callers still get a
// parse error with the full raw text.
func extractJSONValue(s string) string {
	start := strings.IndexAny(s, "{[")
	if start == -1 {
		return s
	}
	open := rune(s[start])
	var close rune
	if open == '{' {
		close = '}'
	} else {
		close = ']'
	}

	depth := 0
	inString := false
	escaped := false
	for i, ch := range s[start:] {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == open {
			depth++
		} else if ch == close {
			depth--
			if depth == 0 {
				return s[start : start+i+1]
			}
		}
	}
	return s
}

func buildCleanTitlesPrompt(requests []models.TitleRequest, repairPass bool) string {
	var sb strings.Builder
	if repairPass {
		sb.WriteString("The previous response omitted some indexes. Clean ONLY these missing deal titles. ")
	} else {
		sb.WriteString("Clean these deal titles. ")
	}
	sb.WriteString("For each input, create a concise title (5-15 words). ")
	sb.WriteString("Remove fluff (\"Lava Hot\", \"Price Error\", \"YMMV\", emojis), store names if redundant, ")
	sb.WriteString("and focus on the product and price/discount.\n\n")

	for _, r := range requests {
		sb.WriteString(fmt.Sprintf("%d. Title: \"%s\"", r.Index, r.Title))
		if r.Retailer != "" {
			sb.WriteString(fmt.Sprintf(" | Retailer: \"%s\"", r.Retailer))
		}
		if r.Price != "" {
			sb.WriteString(fmt.Sprintf(" | Price: \"%s\"", r.Price))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nReturn JSON only. Return exactly one object for every input index above, no omissions and no extra indexes. ")
	sb.WriteString("Use the original input index values exactly: [{\"index\": 0, \"clean_title\": \"...\"}, ...]")
	return sb.String()
}
