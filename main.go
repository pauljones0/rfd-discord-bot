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

	"deedles.dev/transparent"
	"github.com/PuerkitoBio/goquery"
)

const hotDealsURL = "https://forums.redflagdeals.com/hot-deals-f9/"
const discordPurpleColor = 10181046 // #9B59B6

// DealInfo represents the structured information for a deal.
type DealInfo struct {
	PostedTime         string    `firestore:"postedTime"`
	Title              string    `firestore:"title"`
	PostURL            string    `firestore:"postURL"`
	AuthorName         string    `firestore:"authorName"`
	AuthorURL          string    `firestore:"authorURL"`
	ThreadImageURL     string    `firestore:"threadImageURL,omitempty"`
	LikeCount          int       `firestore:"likeCount"`
	CommentCount       int       `firestore:"commentCount"`
	ViewCount          int       `firestore:"viewCount"`
	ActualDealURL      string    `firestore:"actualDealURL,omitempty"`
	FirestoreID        string    `firestore:"-"` // To store the Firestore document ID, not stored in Firestore itself
	DiscordMessageID   string    `firestore:"discordMessageID,omitempty"`
	LastUpdated        time.Time `firestore:"lastUpdated"`
	PublishedTimestamp time.Time `firestore:"publishedTimestamp"` // Parsed from PostedTime
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

// DiscordEmbedFooter represents the footer of a Discord embed.
type DiscordEmbedFooter struct {
	Text    string `json:"text"`
	IconURL string `json:"icon_url,omitempty"`
}

// DiscordEmbed represents a single embed object in a Discord message.
type DiscordEmbed struct {
	Title       string                `json:"title,omitempty"`
	Description string                `json:"description,omitempty"`
	URL         string                `json:"url,omitempty"`       // URL for the title
	Timestamp   string                `json:"timestamp,omitempty"` // ISO8601 timestamp
	Color       int                   `json:"color,omitempty"`     // Decimal color code
	Footer      DiscordEmbedFooter    `json:"footer,omitempty"`
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
	cleanedURL, initialChange := transparent.Clear(rawUrl)

	parsedUrl, err := url.Parse(cleanedURL)
	if err != nil {
		// If parsing fails, return the URL as is from transparent.Clear()
		// and false, as per instruction 4.
		return cleanedURL, false
	}

	if strings.Contains(parsedUrl.Host, "amazon.") {
		queryParams := parsedUrl.Query()
		originalTag := queryParams.Get("tag")
		newTag := "beauahrens0d-20"

		// Check if a change is needed for the tag
		tagChanged := false
		if originalTag != newTag {
			queryParams.Del("tag")
			queryParams.Set("tag", newTag)
			parsedUrl.RawQuery = queryParams.Encode()
			tagChanged = true
		}
		// Return true if either transparent.Clear made a change or the tag was changed.
		return parsedUrl.String(), initialChange || tagChanged
	}

	// For non-Amazon links, return the URL from transparent.Clear()
	// and its change status.
	return cleanedURL, initialChange
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

	// Field 1: Item Link (ActualDealURL)
	if deal.ActualDealURL != "" {
		fields = append(fields, DiscordEmbedField{
			Name:   "Item Link",
			Value:  deal.ActualDealURL,
			Inline: false,
		})
	}

	// Field 2: RFD Post URL
	if deal.PostURL != "" {
		fields = append(fields, DiscordEmbedField{
			Name:   "RFD Post URL",
			Value:  deal.PostURL,
			Inline: true,
		})
	}

	// Field 3: Poster
	if deal.AuthorName != "" {
		fields = append(fields, DiscordEmbedField{
			Name:   "Poster",
			Value:  deal.AuthorName,
			Inline: true,
		})
	}

	// Field 4: Poster URL
	if deal.AuthorURL != "" {
		fields = append(fields, DiscordEmbedField{
			Name:   "Poster URL",
			Value:  deal.AuthorURL, // Clickable URL text
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
	var footerText string
	if !deal.PublishedTimestamp.IsZero() {
		isoTimestamp = deal.PublishedTimestamp.Format(time.RFC3339) // ISO8601
		footerText = fmt.Sprintf("RFD Bot | <t:%d:R>", deal.PublishedTimestamp.Unix())
	} else {
		footerText = "RFD Bot | Timestamp not available"
	}

	return DiscordEmbed{
		Title:       deal.Title,
		Description: description,
		URL:         embedURL,
		Timestamp:   isoTimestamp,
		Color:       discordPurpleColor,
		Footer: DiscordEmbedFooter{
			Text: footerText,
		},
		Thumbnail: thumbnail,
		Fields:    fields,
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

	for i, s := range nonStickyTopics {
		if i < 2 { // Skip the first two non-sticky topics
			title := strings.TrimSpace(s.Find("div:nth-child(2) > div:nth-child(1) > h3:nth-child(2) > a").Text())
			log.Printf("Skipping early non-sticky topic (index %d): %s", i, title)
			continue
		}

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
			deal.LikeCount = safeAtoi(cleanNumericString(likeCountSelection.Text()))
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
		}

		if len(parseErrors) > 0 {
			topicHTML, _ := s.Html()
			log.Printf("Encountered %d parsing issues for deal '%s' (URL: %s): %s. HTML Snippet (max 500 chars): %.500s", len(parseErrors), deal.Title, deal.PostURL, strings.Join(parseErrors, "; "), topicHTML)
		}
		deals = append(deals, deal)
	}

	log.Printf("Successfully processed %d non-sticky deals (after skipping first 2) from %s", len(deals), url)
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

		existingDeal, err := GetDealByPostURL(ctx, fsClient, dealToProcess.PostURL)
		if err != nil {
			errMsg := fmt.Sprintf("Error checking Firestore for deal %s: %v", dealToProcess.PostURL, err)
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
						log.Printf("Preparing to send Discord update for deal '%s', MessageID: %s", updatedFirestoreDeal.Title, updatedFirestoreDeal.DiscordMessageID)
						embedToUpdate := formatDealToEmbed(updatedFirestoreDeal, true) // true for isUpdate
						if err := updateDiscordMessage(discordWebhookURL, updatedFirestoreDeal.DiscordMessageID, embedToUpdate); err != nil {
							errMsg := fmt.Sprintf("Error updating Discord message for deal '%s' (MsgID: %s): %v", updatedFirestoreDeal.Title, updatedFirestoreDeal.DiscordMessageID, err)
							log.Println(errMsg)
							errorMessages = append(errorMessages, errMsg)
						} else {
							log.Printf("Successfully sent Discord update for deal '%s'", updatedFirestoreDeal.Title)
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
					dealToProcess.LastUpdated = time.Now() // Update LastUpdated again after successful send
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
