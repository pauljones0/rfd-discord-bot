package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// knownTwoPartTLDs is a set of common two-part TLDs.
// This list is not exhaustive and for a truly robust solution,
// a library based on the Public Suffix List (PSL) would be preferable.
var knownTwoPartTLDs = map[string]bool{
	"co.uk": true, "com.au": true, "co.jp": true, "co.nz": true, "com.br": true,
	"org.uk": true, "gov.uk": true, "ac.uk": true, "com.cn": true, "net.cn": true,
	"org.cn": true, "co.za": true, "com.es": true, "com.mx": true, "com.sg": true,
	"co.in": true, "ltd.uk": true, "plc.uk": true, "net.au": true, "org.au": true,
	"com.pa": true, "net.pa": true, "org.pa": true, "edu.pa": true, "gob.pa": true,
	"com.py": true, "net.py": true, "org.py": true, "edu.py": true, "gov.py": true,
}

const hotDealsURL = "https://forums.redflagdeals.com/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d"
const discordUpdateInterval = 10 * time.Minute

// Heat ranking and color constants
const (
	colorColdDeal    = 3092790  // #2F3136 (Dark Grey)
	colorWarmDeal    = 16753920 // #FFA500 (Orange)
	colorHotDeal     = 16711680 // #FF0000 (Red)
	colorVeryHotDeal = 16776960 // #FFFFFF (White)

	heatScoreThresholdCold = 0.05
	heatScoreThresholdWarm = 0.1
	heatScoreThresholdHot  = 0.25
)

// DealInfo represents the structured information for a deal.
type DealInfo struct {
	PostedTime             string    `firestore:"postedTime"`
	Title                  string    `firestore:"title"`
	PostURL                string    `firestore:"postURL"`
	AuthorName             string    `firestore:"authorName"`
	AuthorURL              string    `firestore:"authorURL"`
	ThreadImageURL         string    `firestore:"threadImageURL,omitempty"`
	LikeCount              int       `firestore:"likeCount"`
	CommentCount           int       `firestore:"commentCount"`
	ViewCount              int       `firestore:"viewCount"`
	ActualDealURL          string    `firestore:"actualDealURL,omitempty"`
	FirestoreID            string    `firestore:"-"` // To store the Firestore document ID, not stored in Firestore itself
	DiscordMessageID       string    `firestore:"discordMessageID,omitempty"`
	LastUpdated            time.Time `firestore:"lastUpdated"`
	PublishedTimestamp     time.Time `firestore:"publishedTimestamp"` // Parsed from PostedTime
	DiscordLastUpdatedTime time.Time `firestore:"discordLastUpdatedTime,omitempty"`
}

// DiscordWebhookPayload represents the JSON payload for sending a message via Discord webhook.
type DiscordWebhookPayload struct {
	Content string         `json:"content,omitempty"` // Can be empty/null for embed-only messages
	Embeds  []DiscordEmbed `json:"embeds"`
}

// DiscordEmbedThumbnail represents the thumbnail of a Discord embed.
type DiscordEmbedThumbnail struct {
	URL string `json:"url,omitempty"`
}

// DiscordEmbedField represents a field in a Discord embed.
type DiscordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// DiscordEmbed represents a single embed object in a Discord message.
type DiscordEmbed struct {
	Title       string                `json:"title,omitempty"`
	Description string                `json:"description,omitempty"`
	URL         string                `json:"url,omitempty"`       // URL for the title
	Timestamp   string                `json:"timestamp,omitempty"` // ISO8601 timestamp
	Color       int                   `json:"color,omitempty"`     // Decimal color code
	Thumbnail   DiscordEmbedThumbnail `json:"thumbnail,omitempty"`
	Fields      []DiscordEmbedField   `json:"fields,omitempty"`
}

// DiscordMessageResponse is the structure of the message object returned by Discord after a webhook post.
type DiscordMessageResponse struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
}

// cleanReferralLink attempts to clean a URL by removing tracking parameters and
// specifically modifies Amazon links to use a standard affiliate tag.
// It returns the cleaned URL and a boolean indicating if any change was made.
func cleanReferralLink(rawUrl string) (string, bool) {
	parsedUrl, err := url.Parse(rawUrl)
	if err != nil {
		log.Printf("Failed to parse URL '%s': %v. Returning as is.", rawUrl, err)
		return rawUrl, false
	}

	// Best Buy specific constants
	const newBestBuyPrefix = "https://bestbuyca.o93x.net/c/5215192/2035226/10221?u="
	bestBuyRegex := regexp.MustCompile(`^https://bestbuyca\.o93x\.net/c/\d+/\d+/\d+\?u=`)

	switch {
	case parsedUrl.Host == "click.linksynergy.com":
		murlParam := parsedUrl.Query().Get("murl")
		if murlParam != "" {
			decodedMURL, decodeErr := url.QueryUnescape(murlParam)
			if decodeErr == nil {
				return decodedMURL, true
			}
			log.Printf("Failed to process linksynergy URL: murl parameter decode error for %s: %v", rawUrl, decodeErr)
			return rawUrl, false
		}
		log.Printf("Failed to process linksynergy URL: murl parameter missing for %s", rawUrl)
		return rawUrl, false

	case parsedUrl.Host == "go.redirectingat.com":
		urlParam := parsedUrl.Query().Get("url")
		if urlParam != "" {
			decodedDestURL, decodeErr := url.QueryUnescape(urlParam)
			if decodeErr == nil {
				return decodedDestURL, true
			}
			log.Printf("Failed to process redirectingat URL: url parameter decode error for %s: %v", rawUrl, decodeErr)
			return rawUrl, false
		}
		log.Printf("Failed to process redirectingat URL: url parameter missing for %s", rawUrl)
		return rawUrl, false

	case parsedUrl.Host == "bestbuyca.o93x.net" && bestBuyRegex.MatchString(rawUrl):
		// Find the part of the URL after "?u="
		uIndex := strings.Index(rawUrl, "?u=")
		if uIndex == -1 {
			// This case should ideally not be hit if the regex matched, but as a safeguard:
			log.Printf("Best Buy URL matched regex but '?u=' not found: %s", rawUrl)
			return rawUrl, false
		}
		productURLPart := rawUrl[uIndex+len("?u="):]
		cleanedURL := newBestBuyPrefix + productURLPart
		log.Printf("Cleaned Best Buy referral link. Original: %s, New: %s", rawUrl, cleanedURL)
		return cleanedURL, true

	case strings.Contains(parsedUrl.Host, "amazon."):
		queryParams := parsedUrl.Query()
		originalTag := queryParams.Get("tag")
		const newTag = "beauahrens0d-20"
		tagModified := false

		if originalTag != newTag {
			// If there was an old tag and it's different, or if there was no tag, we set the new one.
			// If there was no tag, Get("tag") returns "", so originalTag != newTag will be true.
			// We only consider it a modification if the tag was present and different, or if it was absent.
			// If the tag was already correct, no modification.

			// Check if a "tag" parameter actually existed.
			// If it didn't exist, adding it is a modification.
			// If it existed and was different, changing it is a modification.
			if queryParams.Has("tag") { // Tag existed
				if originalTag != newTag { // And it was different
					queryParams.Del("tag")
					queryParams.Set("tag", newTag)
					tagModified = true
				}
				// If originalTag == newTag, tagModified remains false, no change.
			} else { // Tag did not exist
				queryParams.Set("tag", newTag)
				tagModified = true
			}
		}
		// If tagModified is true, then we update RawQuery and return.
		if tagModified {
			parsedUrl.RawQuery = queryParams.Encode()
			return parsedUrl.String(), true
		}
		// If no modification was made (tag was already correct, or no tag existed and we didn't add one - though this case is covered by the logic above)
		// The instruction: "Return parsedUrl.String(), false (if no change was made to the tag) or parsedUrl.String(), true (if the tag was added/changed)."
		// The boolean `tagModified` correctly captures this.
		return parsedUrl.String(), tagModified // If tagModified is false, it means no change.

	default:
		// No specific domain matched, or no cleaning rules applied that resulted in a change.
		return rawUrl, false
	}
}

// normalizePostURL ensures a consistent format for PostURLs.
// It converts scheme to https, standardizes the host, removes trailing slashes,
// and removes common UTM tracking parameters.
func normalizePostURL(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, fmt.Errorf("failed to parse URL '%s': %w", rawURL, err)
	}

	// 1. Ensure scheme is https
	parsedURL.Scheme = "https"

	// 2. Ensure host is consistent (e.g., forums.redflagdeals.com)
	//    Remove www. if present for forums.redflagdeals.com
	if strings.HasPrefix(parsedURL.Host, "www.") {
		if parsedURL.Host == "www.forums.redflagdeals.com" || parsedURL.Host == "www.redflagdeals.com" {
			parsedURL.Host = strings.TrimPrefix(parsedURL.Host, "www.")
		}
		// Add other www. removal cases if necessary, or make it more generic
	}
	// Ensure it's the canonical host if known (e.g. always forums.redflagdeals.com)
	if parsedURL.Host == "redflagdeals.com" { // Assuming forum links might sometimes miss the subdomain
		parsedURL.Host = "forums.redflagdeals.com"
	}

	// 3. Remove trailing slashes from path
	if len(parsedURL.Path) > 1 && strings.HasSuffix(parsedURL.Path, "/") {
		parsedURL.Path = parsedURL.Path[:len(parsedURL.Path)-1]
	}

	// 4. Remove common UTM tracking parameters
	queryParams := parsedURL.Query()
	utmParams := []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "rfd_sk", "sd", "sk"} // Added RFD specific params
	for _, param := range utmParams {
		if queryParams.Has(param) {
			queryParams.Del(param)
		}
	}
	parsedURL.RawQuery = queryParams.Encode()

	return parsedURL.String(), nil
}

// getHomeDomain extracts the effective top-level domain plus one label (e.g., "example.com", "example.co.uk").
// It attempts to remove subdomains.
// e.g., "https://forums.redflagdeals.com/path" -> "redflagdeals.com"
// e.g., "https://www.example.co.uk/path" -> "example.co.uk"
// Returns "Link" if the URL is malformed or the host is empty.
func getHomeDomain(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		log.Printf("Failed to parse URL '%s' for home domain extraction: %v. Returning default.", rawURL, err)
		return "Link"
	}

	hostname := parsedURL.Hostname() // Use Hostname() to get host without port
	if hostname == "" {
		log.Printf("URL '%s' has an empty hostname. Returning default.", rawURL)
		return "Link"
	}

	// Specific check for bestbuyca.o93x.net, so that it doesn't show up as o93x.net
	if hostname == "bestbuyca.o93x.net" {
		return hostname
	}

	parts := strings.Split(hostname, ".")
	numParts := len(parts)

	if numParts <= 1 { // e.g., "localhost", or an empty string if hostname was just "."
		return hostname // Return hostname as is (e.g., "localhost")
	}

	// Check for known two-part TLDs
	// Example: "example.co.uk" (3 parts), "sub.example.co.uk" (4 parts)
	if numParts >= 3 {
		// Candidate for a two-part TLD is the last two parts
		tldCandidate := parts[numParts-2] + "." + parts[numParts-1]
		if knownTwoPartTLDs[tldCandidate] {
			// The domain part is the one before the two-part TLD
			// parts[numParts-3] is the domain name itself (e.g., "example" from "example.co.uk")
			return parts[numParts-3] + "." + tldCandidate // e.g., "example.co.uk"
		}
	}

	// Default: assume a single-part TLD (e.g., .com, .net, .ca)
	// This will also handle cases like "sub.example.com" or "example.com"
	if numParts >= 2 {
		// The domain part is parts[numParts-2], TLD is parts[numParts-1]
		return parts[numParts-2] + "." + parts[numParts-1] // e.g., "example.com"
	}

	// Fallback: Should ideally not be reached if numParts > 1.
	return hostname // Return the original hostname if logic doesn't simplify it
}

// calculateHeatScore calculates the "heat" of a deal.
// (Likes + Comments) / Views. Returns 0 if Views is 0.
func calculateHeatScore(likes, comments, views int) float64 {
	if views == 0 {
		return 0.0
	}
	return float64(likes+comments) / float64(views)
}

// getHeatColor determines the embed color based on the heat score.
func getHeatColor(heatScore float64) int {
	if heatScore > heatScoreThresholdHot {
		return colorVeryHotDeal
	} else if heatScore > heatScoreThresholdWarm {
		return colorHotDeal
	} else if heatScore > heatScoreThresholdCold {
		return colorWarmDeal
	}
	return colorColdDeal
}

// formatDealToEmbed converts a DealInfo into a DiscordEmbed object.
// isUpdate flag determines the description.
func formatDealToEmbed(deal DealInfo, isUpdate bool) DiscordEmbed {
	var embedURL string
	if deal.ActualDealURL != "" {
		embedURL = deal.ActualDealURL
	} else {
		embedURL = deal.PostURL // Fallback to PostURL if ActualDealURL is empty
	}

	description := "New RFD Post"
	if isUpdate {
		description = "Deal Updated"
	}

	var fields []DiscordEmbedField

	// Field 1: Item (ActualDealURL)
	if deal.ActualDealURL != "" {
		fields = append(fields, DiscordEmbedField{
			Name:   "Item",
			Value:  fmt.Sprintf("[%s](%s)", getHomeDomain(deal.ActualDealURL), deal.ActualDealURL),
			Inline: true,
		})
	}

	// Field 2: Post (RFD Post URL)
	if deal.PostURL != "" {
		fields = append(fields, DiscordEmbedField{
			Name:   "Post",
			Value:  fmt.Sprintf("[%s](%s)", getHomeDomain(deal.PostURL), deal.PostURL),
			Inline: true,
		})
	}

	// Field 3 & 4 combined: Poster Name and Profile URL
	if deal.AuthorName != "" && deal.AuthorURL != "" {
		fields = append(fields, DiscordEmbedField{
			Name:   "Poster",
			Value:  fmt.Sprintf("[%s](%s)", deal.AuthorName, deal.AuthorURL),
			Inline: true,
		})
	}

	// Field 5: Likes
	fields = append(fields, DiscordEmbedField{
		Name:   "Likes",
		Value:  strconv.Itoa(deal.LikeCount),
		Inline: true,
	})

	// Field 6: Comments
	fields = append(fields, DiscordEmbedField{
		Name:   "Comments",
		Value:  strconv.Itoa(deal.CommentCount),
		Inline: true,
	})

	// Field 7: Views
	fields = append(fields, DiscordEmbedField{
		Name:   "Views",
		Value:  strconv.Itoa(deal.ViewCount),
		Inline: true,
	})

	var thumbnail DiscordEmbedThumbnail
	if deal.ThreadImageURL != "" {
		thumbnail.URL = deal.ThreadImageURL
	}

	// Ensure PublishedTimestamp is not zero before formatting
	var isoTimestamp string
	if !deal.PublishedTimestamp.IsZero() {
		isoTimestamp = deal.PublishedTimestamp.Format(time.RFC3339) // ISO8601
	}

	heatScore := calculateHeatScore(deal.LikeCount, deal.CommentCount, deal.ViewCount)
	embedColor := getHeatColor(heatScore)

	return DiscordEmbed{
		Title:       deal.Title,
		Description: description,
		URL:         embedURL,
		Timestamp:   isoTimestamp,
		Color:       embedColor,
		Thumbnail:   thumbnail,
		Fields:      fields,
	}
}

// sendAndGetMessageID sends a single embed to Discord and returns the message ID.
func sendAndGetMessageID(webhookURL string, embed DiscordEmbed) (string, error) {
	if webhookURL == "" {
		return "", fmt.Errorf("discord webhook URL is empty")
	}

	payload := DiscordWebhookPayload{
		Embeds: []DiscordEmbed{embed}, // Send a single embed
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Discord payload: %w", err)
	}

	parsedURL, err := url.Parse(webhookURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse webhook URL '%s': %w", webhookURL, err)
	}
	q := parsedURL.Query()
	q.Set("wait", "true")
	parsedURL.RawQuery = q.Encode()
	finalWebhookURL := parsedURL.String()

	req, err := http.NewRequest("POST", finalWebhookURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create Discord webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send Discord webhook request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return "", fmt.Errorf("failed to read response body from Discord (status: %s): %w", resp.Status, readErr)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var msgResponse DiscordMessageResponse
		if err := json.Unmarshal(bodyBytes, &msgResponse); err != nil {
			return "", fmt.Errorf("failed to unmarshal Discord success response (status: %s, body: %s): %w", resp.Status, string(bodyBytes), err)
		}
		if msgResponse.ID == "" {
			return "", fmt.Errorf("discord response successful but message ID is empty (status: %s, body: %s)", resp.Status, string(bodyBytes))
		}
		return msgResponse.ID, nil
	}

	log.Printf("Failed to send Discord webhook, status: %s, response: %s", resp.Status, string(bodyBytes))
	return "", fmt.Errorf("failed to send Discord webhook, status: %s, response: %s", resp.Status, string(bodyBytes))
}

// updateDiscordMessage sends a PATCH request to update an existing Discord message's embed.
func updateDiscordMessage(webhookURL string, messageID string, embed DiscordEmbed) error {
	if webhookURL == "" {
		return fmt.Errorf("discord webhook URL is empty for update")
	}
	if messageID == "" {
		return fmt.Errorf("discord message ID is empty for update")
	}

	payload := DiscordWebhookPayload{
		Embeds:  []DiscordEmbed{embed}, // Entire embed structure is sent
		Content: "",                    // Ensure content is empty or null if only updating embed
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal Discord update payload: %w", err)
	}

	parsedBaseURL, err := url.Parse(webhookURL)
	if err != nil {
		return fmt.Errorf("failed to parse base webhook URL '%s' for PATCH: %w", webhookURL, err)
	}
	// Path already includes /webhooks/id/token. We append /messages/message_id
	finalPatchURL := fmt.Sprintf("%s://%s%s/messages/%s", parsedBaseURL.Scheme, parsedBaseURL.Host, parsedBaseURL.Path, messageID)

	req, err := http.NewRequest("PATCH", finalPatchURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create Discord webhook PATCH request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send Discord webhook PATCH request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("failed to read response body from Discord PATCH (status: %s): %w", resp.Status, readErr)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 { // Discord returns 200 OK on successful embed update.
		log.Printf("Successfully updated Discord message ID: %s", messageID)
		return nil
	}

	log.Printf("Failed to update Discord message ID %s, status: %s, response: %s", messageID, resp.Status, string(bodyBytes))
	return fmt.Errorf("failed to update Discord message ID %s, status: %s, response: %s", messageID, resp.Status, string(bodyBytes))
}

// fetchHTMLContent fetches HTML from a URL and returns a goquery document.
func fetchHTMLContent(url string) (*goquery.Document, error) {
	res, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL %s: %w", url, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch URL %s: status code %d", url, res.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML from %s: %w", url, err)
	}
	return doc, nil
}

// Helper function to safely convert string to int, returns 0 on error.
func safeAtoi(s string) int {
	s = strings.ReplaceAll(s, ",", "") // Remove commas
	i, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return i
}

// Helper function to clean strings, e.g., remove " views"
var nonNumericRegex = regexp.MustCompile(`[^\d]`)

func cleanNumericString(s string) string {
	return nonNumericRegex.ReplaceAllString(s, "")
}

// Regex to find the first occurrence of a number, possibly with a leading sign.
var extractSignedNumberRegex = regexp.MustCompile(`-?\d+`)

// parseSignedNumericString extracts the first numeric string that might have a leading hyphen.
func parseSignedNumericString(s string) string {
	match := extractSignedNumberRegex.FindString(s)
	return match // If no match, returns empty string, which safeAtoi handles as 0
}

// scrapeDealDetailPage fetches the deal's detail page and extracts the actual deal URL.
func scrapeDealDetailPage(dealURL string) (string, error) {
	log.Printf("Scraping deal detail page: %s", dealURL)
	doc, err := fetchHTMLContent(dealURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch or parse deal detail page %s: %w", dealURL, err)
	}

	var urlA, urlB string
	var existsA, existsB bool

	// Selector A: .get-deal-button
	getDealButton := doc.Find(".get-deal-button")
	if getDealButton.Length() > 0 {
		href, found := getDealButton.Attr("href")
		if found && strings.TrimSpace(href) != "" {
			urlA = strings.TrimSpace(href)
			existsA = true
		}
	}

	// Selector B: a.autolinker_link:nth-child(1)
	autolinkerLink := doc.Find("a.autolinker_link:nth-child(1)")
	if autolinkerLink.Length() > 0 {
		href, found := autolinkerLink.Attr("href")
		if found && strings.TrimSpace(href) != "" {
			trimmedHref := strings.TrimSpace(href)
			if (strings.HasPrefix(trimmedHref, "http://") || strings.HasPrefix(trimmedHref, "https://")) &&
				!strings.Contains(trimmedHref, "redflagdeals.com") {
				urlB = trimmedHref
				existsB = true
			}
		}
	}

	if existsA && existsB {
		if urlA == urlB {
			log.Printf("Found actual deal URL with both selectors (match): %s for %s", urlA, dealURL)
			return urlA, nil
		}
		log.Printf("Deal link mismatch for post %s. Selector A: %s, Selector B: %s. Using Selector A.", dealURL, urlA, urlB)
		return urlA, nil // Use URL from Selector A in case of mismatch
	} else if existsA {
		log.Printf("Found actual deal URL with Selector A only: %s for %s (Selector B not found or invalid)", urlA, dealURL)
		return urlA, nil
	} else if existsB {
		log.Printf("Found actual deal URL with Selector B only: %s for %s (Selector A not found)", urlB, dealURL)
		return urlB, nil
	}

	log.Printf("No actual deal URL found for %s using specified selectors.", dealURL)
	return "", nil // No error, just means URL wasn't found
}

// scrapeHotDealsPage fetches and parses the hot deals page.
func scrapeHotDealsPage(url string) ([]DealInfo, error) {
	log.Printf("Scraping hot deals page: %s", url)
	doc, err := fetchHTMLContent(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch or parse hot deals page %s: %w", url, err)
	}

	if doc.Find("li.topic").Length() == 0 {
		bodyHTML, _ := doc.Find("body").Html()
		var snippet string
		if len(bodyHTML) > 200 {
			snippet = bodyHTML[:200]
		} else {
			snippet = bodyHTML
		}
		log.Printf("Warning: No 'li.topic' elements found on %s. Potential block or page structure change. Body snippet: %s", url, snippet)
		return nil, fmt.Errorf("no 'li.topic' elements found on %s. Potential block or page structure change", url)
	}

	var deals []DealInfo
	var allTopics []*goquery.Selection
	doc.Find("li.topic").Each(func(_ int, s *goquery.Selection) {
		allTopics = append(allTopics, s)
	})

	var nonStickyTopics []*goquery.Selection
	for _, s := range allTopics {
		if !(s.HasClass("sticky")) {
			nonStickyTopics = append(nonStickyTopics, s)
		}
	}
	log.Printf("DEBUG: Found %d total 'li.topic' elements, %d non-sticky/non-sponsored.", len(allTopics), len(nonStickyTopics))

	for _, s := range nonStickyTopics {
		var deal DealInfo
		var parseErrors []string

		// 1. Posted Time: li.topic:nth-child(n+3) > div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(1) > time
		timeSelection := s.Find("div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(1) > time")
		if timeSelection.Length() > 0 {
			deal.PostedTime = strings.TrimSpace(timeSelection.Text()) // Text content
			datetimeStr, exists := timeSelection.Attr("datetime")
			if exists {
				deal.PostedTime = datetimeStr // Prefer datetime attribute for parsing
				parsedTime, err := time.Parse(time.RFC3339, datetimeStr)
				if err == nil {
					deal.PublishedTimestamp = parsedTime
				} else {
					parseErrors = append(parseErrors, fmt.Sprintf("failed to parse datetime string '%s': %v", datetimeStr, err))
				}
			} else {
				parseErrors = append(parseErrors, "time element 'datetime' attribute missing")
			}
		} else {
			parseErrors = append(parseErrors, "posted time element not found with selector")
		}

		// 2. Thread Title Link & Text: li.topic:nth-child(n+3) > div:nth-child(2) > div:nth-child(1) > h3:nth-child(2) > a
		titleLinkSelection := s.Find("div:nth-child(2) > div:nth-child(1) > h3:nth-child(2) > a")
		if titleLinkSelection.Length() > 0 {
			deal.Title = strings.TrimSpace(titleLinkSelection.Text())
			postURL, exists := titleLinkSelection.Attr("href")
			if exists {
				if strings.HasPrefix(postURL, "/") {
					deal.PostURL = "https://forums.redflagdeals.com" + postURL
				} else {
					deal.PostURL = postURL
				}
				// Normalize the PostURL
				if deal.PostURL != "" {
					normalizedURL, normErr := normalizePostURL(deal.PostURL)
					if normErr != nil {
						log.Printf("Warning: Failed to normalize PostURL '%s': %v. Using original.", deal.PostURL, normErr)
						// Optionally add to parseErrors: parseErrors = append(parseErrors, fmt.Sprintf("PostURL normalization error: %v", normErr))
					} else {
						if deal.PostURL != normalizedURL {
							log.Printf("Normalized PostURL from '%s' to '%s'", deal.PostURL, normalizedURL)
						}
						deal.PostURL = normalizedURL
					}
				}
			} else {
				parseErrors = append(parseErrors, "post URL href attribute missing")
			}
		} else {
			parseErrors = append(parseErrors, "title/post URL element not found with selector")
		}

		// 3. Author Profile Link: li.topic:nth-child(n+3) > div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(1) > a:nth-child(1)
		authorLinkSelection := s.Find("div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(1) > a:nth-child(1)")
		if authorLinkSelection.Length() > 0 {
			authorURL, exists := authorLinkSelection.Attr("href")
			if exists {
				if strings.HasPrefix(authorURL, "/") {
					deal.AuthorURL = "https://forums.redflagdeals.com" + authorURL
				} else {
					deal.AuthorURL = authorURL
				}
			} else {
				parseErrors = append(parseErrors, "author URL href attribute missing")
			}
			// 4. Author Name: li.topic:nth-child(n+3) > div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(1) > a:nth-child(1) > span:nth-child(2)
			authorNameSelection := authorLinkSelection.Find("span:nth-child(2)")
			if authorNameSelection.Length() > 0 {
				deal.AuthorName = strings.TrimSpace(authorNameSelection.Text())
			} else {
				// Fallback if specific span not found, try text of link itself (though less precise)
				deal.AuthorName = strings.TrimSpace(authorLinkSelection.Text())
				if deal.AuthorName == "" {
					parseErrors = append(parseErrors, "author name text missing from span and link")
				} else {
					parseErrors = append(parseErrors, "author name span not found, used link text as fallback")
				}
			}
		} else {
			parseErrors = append(parseErrors, "author link element not found with selector")
		}

		// 5. Thread Image URL (Optional): li.topic:nth-child(n+3) > div:nth-child(2) > div:nth-child(2) > img
		imgSelection := s.Find("div:nth-child(2) > div:nth-child(2) > img")
		if imgSelection.Length() > 0 {
			src, exists := imgSelection.Attr("src")
			if exists {
				deal.ThreadImageURL = src
			}
		}

		// 6. Like Count: li.topic:nth-child(n+3) > div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(3) > span
		likeCountSelection := s.Find("div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(3) > span")
		if likeCountSelection.Length() > 0 {
			deal.LikeCount = safeAtoi(parseSignedNumericString(likeCountSelection.Text()))
		} else {
			deal.LikeCount = 0
			parseErrors = append(parseErrors, "like count element not found with selector")
		}

		// 7. Comment Count: li.topic:nth-child(n+3) > div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(5) > span:nth-child(2)
		commentCountSelection := s.Find("div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(5) > span:nth-child(2)")
		if commentCountSelection.Length() > 0 {
			deal.CommentCount = safeAtoi(cleanNumericString(commentCountSelection.Text()))
		} else {
			deal.CommentCount = 0
			parseErrors = append(parseErrors, "comment count element not found with selector")
		}

		// 8. View Count: li.topic:nth-child(n+3) > div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(7)
		viewCountSelection := s.Find("div:nth-child(2) > div:nth-child(1) > div:nth-child(3) > div:nth-child(7)")
		if viewCountSelection.Length() > 0 {
			deal.ViewCount = safeAtoi(cleanNumericString(viewCountSelection.Text())) // " views" suffix handled by cleanNumericString
		} else {
			deal.ViewCount = 0
			parseErrors = append(parseErrors, "view count element not found with selector")
		}

		if deal.PostURL != "" {
			actualURL, detailErr := scrapeDealDetailPage(deal.PostURL)
			if detailErr != nil {
				log.Printf("Error scraping detail page for %s: %v. Continuing without actual deal URL.", deal.PostURL, detailErr)
				// parseErrors = append(parseErrors, fmt.Sprintf("detail page scrape error: %v", detailErr)) // Optionally log as parse error
			}
			deal.ActualDealURL = actualURL
			if deal.ActualDealURL != "" {
				cleanedURL, changed := cleanReferralLink(deal.ActualDealURL)
				if changed {
					log.Printf("Cleaned referral link for %s (original: %s, cleaned: %s)", deal.PostURL, deal.ActualDealURL, cleanedURL)
				}
				deal.ActualDealURL = cleanedURL
			}
			// If ActualDealURL is still empty after parsing and cleaning, set a default, so that the Field shows up in the Embed and looks nice. Get Rick Roll'd.
			if deal.ActualDealURL == "" {
				log.Printf("ActualDealURL for %s was empty after parsing, setting default URL.", deal.PostURL)
				deal.ActualDealURL = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
			}
		}

		if len(parseErrors) > 0 {
			topicHTML, _ := s.Html()
			log.Printf("Encountered %d parsing issues for deal '%s' (URL: %s): %s. HTML Snippet (max 500 chars): %.500s", len(parseErrors), deal.Title, deal.PostURL, strings.Join(parseErrors, "; "), topicHTML)
		}
		deals = append(deals, deal)
	}

	log.Printf("Successfully processed %d non-sticky deals from %s", len(deals), url)
	return deals, nil
}

// ProcessDealsHandler is the HTTP handler for processing RFD deals.
func ProcessDealsHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("ProcessDealsHandler invoked.")
	var handlerProcessingError error
	var errorMessages []string // To collect multiple error messages for a final summary

	ctx := context.Background()
	discordWebhookURL := os.Getenv("DISCORD_WEBHOOK_URL")
	if discordWebhookURL == "" {
		log.Println("Warning: DISCORD_WEBHOOK_URL environment variable not set. Discord notifications will be skipped.")
	}

	log.Println("Initializing Firestore client...")
	fsClient, err := initFirestoreClient(ctx)
	if err != nil {
		log.Printf("Critical error initializing Firestore client: %v", err)
		http.Error(w, "Failed to initialize Firestore client", http.StatusInternalServerError)
		return
	}
	defer fsClient.Close()
	log.Println("Successfully initialized Firestore client.")

	log.Println("Fetching RFD Hot Deals page via scraping...")
	scrapedDeals, err := scrapeHotDealsPage(hotDealsURL)
	if err != nil {
		log.Printf("Critical error scraping hot deals page: %v", err)
		http.Error(w, fmt.Sprintf("Failed to scrape hot deals page: %v", err), http.StatusInternalServerError)
		return
	}
	log.Printf("Successfully scraped %d deals from page.", len(scrapedDeals))

	var newDealsCount, updatedDealsCount int

	log.Println("Processing scraped deals...")
	for _, currentScrapedDeal := range scrapedDeals {
		dealToProcess := currentScrapedDeal // Make a mutable copy
		log.Printf("Processing deal: %s (URL: %s)", dealToProcess.Title, dealToProcess.PostURL)

		// Ensure the PostURL used for lookup is normalized.
		// dealToProcess.PostURL should already be normalized from scrapeHotDealsPage.
		// If there's any doubt or if it could be sourced elsewhere unnormalized,
		// re-normalizing here would be safest, though potentially redundant.
		// For now, we trust it's normalized from the scraping stage.
		// If GetDealByPostURL needs to be absolutely certain, it could normalize its input.
		// However, the instruction is to normalize it *before* passing to GetDealByPostURL.
		// Since dealToProcess.PostURL is already the result of normalization from the scrape,
		// we can use it directly.

		lookupURL := dealToProcess.PostURL // This should be the normalized URL from scraping.
		// If we wanted to be absolutely sure and re-normalize (as per a strict reading of "applied to the scraped dealToProcess.PostURL"):
		// normalizedLookupURL, normErr := normalizePostURL(dealToProcess.PostURL)
		// if normErr != nil {
		// 	log.Printf("Error normalizing lookup PostURL '%s': %v. Using original for lookup.", dealToProcess.PostURL, normErr)
		// 	lookupURL = dealToProcess.PostURL // Fallback to potentially unnormalized if re-normalization fails
		// } else {
		// 	lookupURL = normalizedLookupURL
		// }

		existingDeal, err := GetDealByPostURL(ctx, fsClient, lookupURL)
		if err != nil {
			errMsg := fmt.Sprintf("Error checking Firestore for deal %s: %v", lookupURL, err)
			log.Println(errMsg)
			errorMessages = append(errorMessages, errMsg)
			continue // Skip this deal and process others
		}

		if existingDeal != nil { // Deal Exists in Firestore
			log.Printf("Deal '%s' (ID: %s) already exists. DiscordMsgID: '%s'. Checking for updates.", existingDeal.Title, existingDeal.FirestoreID, existingDeal.DiscordMessageID)

			updateNeeded := false
			if existingDeal.LikeCount != dealToProcess.LikeCount ||
				existingDeal.CommentCount != dealToProcess.CommentCount ||
				existingDeal.ViewCount != dealToProcess.ViewCount ||
				existingDeal.Title != dealToProcess.Title ||
				existingDeal.ActualDealURL != dealToProcess.ActualDealURL ||
				existingDeal.ThreadImageURL != dealToProcess.ThreadImageURL {
				updateNeeded = true
			}

			// Prepare the deal for Firestore update by copying new data from scrape over existing
			// This ensures we preserve FirestoreID and DiscordMessageID from existingDeal
			updatedFirestoreDeal := *existingDeal
			updatedFirestoreDeal.Title = dealToProcess.Title
			updatedFirestoreDeal.ThreadImageURL = dealToProcess.ThreadImageURL
			updatedFirestoreDeal.LikeCount = dealToProcess.LikeCount
			updatedFirestoreDeal.CommentCount = dealToProcess.CommentCount
			updatedFirestoreDeal.ViewCount = dealToProcess.ViewCount
			updatedFirestoreDeal.ActualDealURL = dealToProcess.ActualDealURL
			// Ensure PublishedTimestamp is from the current scrape if valid
			if !dealToProcess.PublishedTimestamp.IsZero() {
				updatedFirestoreDeal.PublishedTimestamp = dealToProcess.PublishedTimestamp
			}
			// AuthorName, AuthorURL, PostURL are also updated from scrape if they changed
			updatedFirestoreDeal.AuthorName = dealToProcess.AuthorName
			updatedFirestoreDeal.AuthorURL = dealToProcess.AuthorURL
			// PostedTime is also from scrape
			updatedFirestoreDeal.PostedTime = dealToProcess.PostedTime

			updatedFirestoreDeal.LastUpdated = time.Now()

			if _, err := WriteDealInfo(ctx, fsClient, updatedFirestoreDeal); err != nil {
				errMsg := fmt.Sprintf("Error updating existing deal '%s' (ID: %s) in Firestore: %v", updatedFirestoreDeal.Title, updatedFirestoreDeal.FirestoreID, err)
				log.Println(errMsg)
				errorMessages = append(errorMessages, errMsg)
			} else {
				log.Printf("Successfully updated existing deal '%s' (ID: %s) in Firestore.", updatedFirestoreDeal.Title, updatedFirestoreDeal.FirestoreID)
				if updateNeeded {
					updatedDealsCount++
					log.Printf("Deal '%s' had data changes. Likes: %d->%d, Comments: %d->%d, Views: %d->%d, Title: '%s'->'%s', ActualURL: '%s'->'%s', ImageURL: '%s'->'%s'",
						updatedFirestoreDeal.Title,
						existingDeal.LikeCount, updatedFirestoreDeal.LikeCount,
						existingDeal.CommentCount, updatedFirestoreDeal.CommentCount,
						existingDeal.ViewCount, updatedFirestoreDeal.ViewCount,
						existingDeal.Title, updatedFirestoreDeal.Title,
						existingDeal.ActualDealURL, updatedFirestoreDeal.ActualDealURL,
						existingDeal.ThreadImageURL, updatedFirestoreDeal.ThreadImageURL)

					if discordWebhookURL != "" && updatedFirestoreDeal.DiscordMessageID != "" {
						if time.Since(existingDeal.DiscordLastUpdatedTime) >= discordUpdateInterval {
							log.Printf("Preparing to send Discord update for deal '%s', MessageID: %s. Interval passed.", updatedFirestoreDeal.Title, updatedFirestoreDeal.DiscordMessageID)
							embedToUpdate := formatDealToEmbed(updatedFirestoreDeal, true) // true for isUpdate
							if err := updateDiscordMessage(discordWebhookURL, updatedFirestoreDeal.DiscordMessageID, embedToUpdate); err != nil {
								errMsg := fmt.Sprintf("Error updating Discord message for deal '%s' (MsgID: %s): %v", updatedFirestoreDeal.Title, updatedFirestoreDeal.DiscordMessageID, err)
								log.Println(errMsg)
								errorMessages = append(errorMessages, errMsg)
							} else {
								log.Printf("Successfully sent Discord update for deal '%s'", updatedFirestoreDeal.Title)
								updatedFirestoreDeal.DiscordLastUpdatedTime = time.Now() // Update timestamp after successful Discord update
								// Re-write to Firestore to save the DiscordLastUpdatedTime
								if _, err := WriteDealInfo(ctx, fsClient, updatedFirestoreDeal); err != nil {
									errMsg := fmt.Sprintf("Error updating deal '%s' (ID: %s) in Firestore after Discord update: %v", updatedFirestoreDeal.Title, updatedFirestoreDeal.FirestoreID, err)
									log.Println(errMsg)
									errorMessages = append(errorMessages, errMsg)
									// Note: The main WriteDealInfo earlier in the loop still runs, this is an additional one for the timestamp.
									// Consider if this needs to be merged or if the main one should be conditional / delayed.
									// For now, this ensures DiscordLastUpdatedTime is saved if the Discord update was successful.
								} else {
									log.Printf("Successfully updated DiscordLastUpdatedTime for deal '%s' (ID: %s) in Firestore.", updatedFirestoreDeal.Title, updatedFirestoreDeal.FirestoreID)
								}
							}
						} else {
							log.Printf("Skipping Discord update for deal '%s' (MsgID: %s) due to 10-minute interval. Last updated: %s", updatedFirestoreDeal.Title, updatedFirestoreDeal.DiscordMessageID, existingDeal.DiscordLastUpdatedTime.Format(time.RFC3339))
						}
					} else if discordWebhookURL == "" {
						log.Println("DISCORD_WEBHOOK_URL not set, skipping Discord update for existing deal.")
					} else if updatedFirestoreDeal.DiscordMessageID == "" {
						log.Printf("Deal '%s' updated in Firestore, but no DiscordMessageID found. Cannot send Discord update. Consider sending as new.", updatedFirestoreDeal.Title)
					}
				} else {
					log.Printf("Deal '%s' (ID: %s) checked, no data changes, LastUpdated refreshed.", updatedFirestoreDeal.Title, updatedFirestoreDeal.FirestoreID)
				}
			}
		} else { // Deal is New
			log.Printf("Deal '%s' is new. Adding to Firestore and sending to Discord.", dealToProcess.Title)
			dealToProcess.LastUpdated = time.Now() // Initial LastUpdated

			if discordWebhookURL != "" {
				log.Printf("Formatting and sending new deal to Discord: '%s'", dealToProcess.Title)
				newDealEmbed := formatDealToEmbed(dealToProcess, false) // false for isUpdate

				messageID, sendErr := sendAndGetMessageID(discordWebhookURL, newDealEmbed)
				if sendErr != nil {
					errMsg := fmt.Sprintf("Error sending new deal '%s' to Discord: %v", dealToProcess.Title, sendErr)
					log.Println(errMsg)
					errorMessages = append(errorMessages, errMsg)
					// Continue to save to Firestore even if Discord send fails
				} else {
					log.Printf("New deal '%s' sent to Discord. Message ID: %s", dealToProcess.Title, messageID)
					dealToProcess.DiscordMessageID = messageID
					dealToProcess.DiscordLastUpdatedTime = time.Now() // Set initial Discord update timestamp
					// dealToProcess.LastUpdated = time.Now() // This is already set before this block, and again if Discord send is successful. Redundant here.
				}
			} else {
				log.Println("DISCORD_WEBHOOK_URL not set, skipping Discord notification for new deal.")
			}

			// Write the new deal to Firestore (with or without DiscordMessageID)
			firestoreID, writeErr := WriteDealInfo(ctx, fsClient, dealToProcess)
			if writeErr != nil {
				errMsg := fmt.Sprintf("Error writing new deal '%s' to Firestore: %v", dealToProcess.Title, writeErr)
				log.Println(errMsg)
				errorMessages = append(errorMessages, errMsg)
			} else {
				// dealToProcess.FirestoreID = firestoreID // FirestoreID is already part of dealToProcess if WriteDealInfo sets it (it doesn't, it returns it)
				log.Printf("New deal '%s' (ID: %s) added to Firestore. DiscordMessageID: '%s'.", dealToProcess.Title, firestoreID, dealToProcess.DiscordMessageID)
				newDealsCount++
			}
		}
	}
	log.Printf("Finished processing loop for scraped deals. New deals found: %d, Existing deals that had data changes: %d.", newDealsCount, updatedDealsCount)

	if len(errorMessages) > 0 {
		handlerProcessingError = fmt.Errorf("%s", strings.Join(errorMessages, "; "))
	}

	if handlerProcessingError != nil {
		log.Printf("ProcessDealsHandler completed with errors: %v", handlerProcessingError)
		http.Error(w, fmt.Sprintf("Deals processed with some errors: %v", handlerProcessingError), http.StatusInternalServerError)
		return
	}

	fmt.Fprintln(w, "Deals processed successfully.")
	log.Println("ProcessDealsHandler completed successfully.")
}

func main() {
	log.Println("Starting RFD Hot Deals Bot server...")
	http.HandleFunc("/", ProcessDealsHandler)              // Default path
	http.HandleFunc("/process-deals", ProcessDealsHandler) // Explicit path for clarity if needed

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("Defaulting to port %s", port)
	}

	log.Printf("Listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Failed to listen and serve: %v", err)
	}
}
