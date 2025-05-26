package main

import (
	"bytes"         // For creating a buffer for the JSON payload
	"context"       // Required for Firestore operations
	"encoding/json" // For marshaling the Discord payload
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os" // For reading environment variables
	"regexp"
	"strings"
	"time"
)

const rssFeedURL = "https://forums.redflagdeals.com/feed/forum/9"

// Feed represents the top-level structure of the RSS feed.
type Feed struct {
	XMLName xml.Name `xml:"feed"`
	Entries []Entry  `xml:"entry"`
}

// Entry represents an individual item in the RSS feed.
type Entry struct {
	XMLName   xml.Name `xml:"entry"`
	Title     string   `xml:"title"`
	Link      Link     `xml:"link"`
	ID        string   `xml:"id"`
	Author    Author   `xml:"author"`
	Published string   `xml:"published"`
	Updated   string   `xml:"updated"`
	Content   Content  `xml:"content"`
}

// Link represents the href attribute of a link tag.
type Link struct {
	Href string `xml:"href,attr"`
}

// Author represents the author of an entry.
type Author struct {
	Name string `xml:"name"`
}

// Content represents the content of an entry.
type Content struct {
	Type    string `xml:"type,attr"`
	XMLBase string `xml:"base,attr"`
	Body    string `xml:",cdata"` // Changed to ,cdata to handle CDATA, also consider innerxml if CDATA is not always present
}

// ProcessedDeal represents the structured information extracted from an RSS entry.
type ProcessedDeal struct {
	Title              string
	Link               string // RFD Post URL
	ID                 string
	Author             string
	PublishedTimestamp time.Time
	ItemLink           string // Direct link to the product
}

// DiscordWebhookPayload represents the JSON payload for sending a message via Discord webhook.
type DiscordWebhookPayload struct {
	Embeds []DiscordEmbed `json:"embeds"`
}

// DiscordEmbed represents a single embed object in a Discord message.
type DiscordEmbed struct {
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	URL         string             `json:"url,omitempty"` // URL for the title
	Footer      DiscordEmbedFooter `json:"footer,omitempty"`
	// Timestamp   string             `json:"timestamp,omitempty"` // ISO8601 timestamp
	// Color       int                `json:"color,omitempty"`     // Decimal color code
}

// DiscordEmbedFooter represents the footer of a Discord embed.
type DiscordEmbedFooter struct {
	Text string `json:"text"`
	// IconURL string `json:"icon_url,omitempty"`
}

// formatDealToEmbed converts a ProcessedDeal into a DiscordEmbed object.
func formatDealToEmbed(deal ProcessedDeal) DiscordEmbed {
	var embedURL string
	if deal.ItemLink != "" {
		embedURL = deal.ItemLink
	} else if deal.Link != "" {
		embedURL = deal.Link
	}

	var descriptionLines []string
	if deal.ItemLink != "" {
		descriptionLines = append(descriptionLines, fmt.Sprintf("Item Link: [%s](%s)", deal.ItemLink, deal.ItemLink))
	}
	descriptionLines = append(descriptionLines, fmt.Sprintf("RFD Post: [%s](%s)", deal.Link, deal.Link))
	descriptionLines = append(descriptionLines, fmt.Sprintf("Poster: %s", deal.Author))

	return DiscordEmbed{
		Title:       deal.Title,
		URL:         embedURL,
		Description: strings.Join(descriptionLines, "\n"),
		Footer: DiscordEmbedFooter{
			Text: fmt.Sprintf("Posted: %s", deal.PublishedTimestamp.Format("Jan 2, 2006 03:04 PM MST")),
		},
	}
}

// sendEmbedsToDiscord sends a list of embeds to the specified Discord webhook URL.
// Embeds are sent in chunks of up to 10 to comply with Discord API limits.
func sendEmbedsToDiscord(webhookURL string, embeds []DiscordEmbed) error {
	if webhookURL == "" {
		return fmt.Errorf("discord webhook URL is empty. Skipping sending embeds")
	}
	if len(embeds) == 0 {
		log.Println("No embeds to send to Discord.")
		return nil
	}

	const maxEmbedsPerRequest = 10
	totalEmbeds := len(embeds)
	sentEmbedsCount := 0

	for i := 0; i < totalEmbeds; i += maxEmbedsPerRequest {
		end := i + maxEmbedsPerRequest
		if end > totalEmbeds {
			end = totalEmbeds
		}
		chunk := embeds[i:end]

		if len(chunk) == 0 {
			continue // Should not happen if logic is correct, but good for safety
		}

		payload := DiscordWebhookPayload{
			Embeds: chunk,
		}

		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			// Log the error and continue to try sending other chunks if any
			log.Printf("Failed to marshal Discord payload for a chunk: %v. Skipping this chunk.", err)
			// Optionally, accumulate errors and return a summary error at the end
			// For now, we'll return the first critical error or nil if all chunks (attempted) are fine.
			// If marshalling fails, it's a significant issue with the data itself.
			return fmt.Errorf("failed to marshal Discord payload for chunk starting at index %d: %w", i, err)
		}

		req, err := http.NewRequest("POST", webhookURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			log.Printf("Failed to create Discord webhook request for a chunk: %v. Skipping this chunk.", err)
			return fmt.Errorf("failed to create Discord webhook request for chunk starting at index %d: %w", i, err)
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Failed to send Discord webhook request for a chunk: %v. Skipping this chunk.", err)
			// Consider if we should retry or accumulate errors. For now, return on first send error.
			return fmt.Errorf("failed to send Discord webhook request for chunk starting at index %d: %w", i, err)
		}
		defer resp.Body.Close() // Defer inside loop is okay as Body is typically small or read immediately

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			log.Printf("Successfully sent chunk of %d embed(s) to Discord (Total sent so far: %d/%d). Status: %s", len(chunk), sentEmbedsCount+len(chunk), totalEmbeds, resp.Status)
			sentEmbedsCount += len(chunk)
		} else {
			bodyBytes, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				log.Printf("Failed to read error response body from Discord for a chunk: %v", readErr)
				return fmt.Errorf("failed to send Discord webhook for chunk (status: %s), and also failed to read response body: %w", resp.Status, readErr)
			}
			log.Printf("Failed to send Discord webhook for a chunk, status: %s, response: %s", resp.Status, string(bodyBytes))
			// Return an error indicating which chunk failed.
			return fmt.Errorf("failed to send Discord webhook for chunk starting at index %d, status: %s, response: %s", i, resp.Status, string(bodyBytes))
		}

		// Optional: Add a small delay between requests if rate limiting becomes an issue, though Discord's webhook limits are usually per second.
		// time.Sleep(500 * time.Millisecond)
	}

	log.Printf("Finished sending all embeds. Total successfully sent: %d/%d.", sentEmbedsCount, totalEmbeds)
	return nil
}

// fetchRSSFeed fetches the RSS feed content from the given URL.
func fetchRSSFeed(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch RSS feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch RSS feed: status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read RSS feed body: %w", err)
	}
	return body, nil
}

// parseRSSFeed parses the XML content and returns a slice of ProcessedDeal.
func parseRSSFeed(xmlData []byte) ([]ProcessedDeal, error) {
	var feed Feed
	if err := xml.Unmarshal(xmlData, &feed); err != nil {
		return nil, fmt.Errorf("failed to unmarshal XML: %w", err)
	}

	var deals []ProcessedDeal
	// Corrected regex to use literal < and > for matching actual HTML in CDATA
	itemLinkRegex := regexp.MustCompile(`<a\s+[^>]*class="postlink"[^>]*\s+href="([^"]+)"`)

	for _, entry := range feed.Entries {
		publishedTime, err := time.Parse(time.RFC3339, entry.Published)
		if err != nil {
			// Try parsing with a slightly different format if the first fails (sometimes observed)
			publishedTime, err = time.Parse("2006-01-02T15:04:05-07:00", entry.Published)
			if err != nil {
				log.Printf("Warning: Failed to parse published timestamp for entry ID %s ('%s'): %v. Skipping entry.", entry.ID, entry.Published, err)
				continue
			}
		}

		var itemLink string
		// Corrected regex to look for <a ... class="postlink" ... href="...">
		// The content might be HTML escaped, so we need to be careful.
		// For simplicity, we'll assume it's not double-escaped in a way that breaks this.
		// A more robust solution might involve an HTML parser if regex becomes too complex.
		matches := itemLinkRegex.FindStringSubmatch(entry.Content.Body)
		if len(matches) > 1 {
			itemLink = matches[1]
			// Basic unescaping for & to &
			itemLink = strings.ReplaceAll(itemLink, "&amp;", "&") // Handle potential double encoding of ampersand
			itemLink = strings.ReplaceAll(itemLink, "&", "&")     // Unescape ampersand
		} else {
			itemLink = entry.Link.Href // Fallback to RFD post link
		}

		deal := ProcessedDeal{
			Title:              entry.Title,
			Link:               entry.Link.Href,
			ID:                 entry.ID,
			Author:             entry.Author.Name,
			PublishedTimestamp: publishedTime,
			ItemLink:           itemLink,
		}
		deals = append(deals, deal)
	}
	return deals, nil
}

// filterDeal applies filtering logic to a deal.
func filterDeal(deal ProcessedDeal) bool {
	// Mandatory Filter: Exclude "Sponsored" deals (case-insensitive)
	if strings.Contains(strings.ToLower(deal.Title), "sponsored") {
		return false
	}

	// TODO: Implement province filtering if needed.
	// Example:
	// provincesToInclude := []string{"SK", "MB"} // User-configurable
	// titleContainsProvince := false
	// for _, prov := range provincesToInclude {
	//     if strings.Contains(deal.Title, prov) {
	//         titleContainsProvince = true
	//         break
	//     }
	// }
	// if !titleContainsProvince && (strings.Contains(deal.Title, "ON") || strings.Contains(deal.Title, "BC") /* ... other provinces ... */) {
	//     return false // Filter out if other provinces are mentioned and ours are not
	// }

	return true
}

// ProcessDealsHandler is the HTTP handler for processing RFD deals.
func ProcessDealsHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("ProcessDealsHandler invoked.")
	var handlerProcessingError error // To accumulate non-fatal errors for final response

	ctx := context.Background()

	discordWebhookURL := os.Getenv("DISCORD_WEBHOOK_URL")
	if discordWebhookURL == "" {
		log.Println("Warning: DISCORD_WEBHOOK_URL environment variable not set. Discord notifications will be skipped.")
		// Not setting handlerProcessingError here as it's a configuration warning, not a processing failure.
	}

	// Initialize Firestore client
	log.Println("Initializing Firestore client...")
	fsClient, err := initFirestoreClient(ctx) // Assumes initFirestoreClient is defined (e.g., in firestore_client.go)
	if err != nil {
		log.Printf("Critical error initializing Firestore client: %v", err)
		http.Error(w, "Failed to initialize Firestore client", http.StatusInternalServerError)
		return
	}
	defer fsClient.Close()
	log.Println("Successfully initialized Firestore client.")

	// Read the last processed timestamp from Firestore
	log.Println("Reading last processed timestamp from Firestore...")
	lastProcessedTimestamp, err := readLastProcessedTimestamp(ctx, fsClient) // Assumes readLastProcessedTimestamp is defined
	if err != nil {
		log.Printf("Critical error reading last processed timestamp: %v", err)
		http.Error(w, "Failed to read last processed timestamp", http.StatusInternalServerError)
		return
	}
	if lastProcessedTimestamp.IsZero() {
		log.Println("No previous timestamp found in Firestore (or it was zero). Will process all fetched deals as new.")
	} else {
		log.Printf("Last processed timestamp read from Firestore: %s", lastProcessedTimestamp.Format(time.RFC3339))
	}

	log.Println("Fetching RFD Hot Deals RSS feed...")
	xmlData, err := fetchRSSFeed(rssFeedURL)
	if err != nil {
		log.Printf("Critical error fetching RSS feed: %v", err)
		http.Error(w, "Failed to fetch RSS feed", http.StatusInternalServerError)
		return
	}
	log.Println("Successfully fetched RSS feed.")

	log.Println("Parsing RSS feed...")
	allDeals, err := parseRSSFeed(xmlData)
	if err != nil {
		log.Printf("Critical error parsing RSS feed: %v", err)
		http.Error(w, "Failed to parse RSS feed", http.StatusInternalServerError)
		return
	}
	log.Printf("Successfully parsed %d deals from feed.\n", len(allDeals))

	log.Println("Filtering deals and identifying new ones...")
	var newDeals []ProcessedDeal
	latestNewDealTimestamp := lastProcessedTimestamp

	for _, deal := range allDeals {
		if filterDeal(deal) {
			if deal.PublishedTimestamp.After(lastProcessedTimestamp) {
				newDeals = append(newDeals, deal)
				if deal.PublishedTimestamp.After(latestNewDealTimestamp) {
					latestNewDealTimestamp = deal.PublishedTimestamp
				}
			}
		}
	}

	if len(newDeals) > 0 {
		log.Printf("Found %d new deals to process.", len(newDeals))

		var embedsToSend []DiscordEmbed
		for _, deal := range newDeals {
			log.Printf("Formatting deal for Discord: %s", deal.Title)
			embedsToSend = append(embedsToSend, formatDealToEmbed(deal))
		}

		if len(embedsToSend) > 0 {
			log.Printf("Sending %d embeds to Discord...", len(embedsToSend))
			err := sendEmbedsToDiscord(discordWebhookURL, embedsToSend)
			if err != nil {
				log.Printf("Error sending embeds to Discord: %v", err)
				handlerProcessingError = fmt.Errorf("failed to send to Discord: %w", err)
			}
		} else {
			// This case should ideally not be reached if newDeals > 0 and formatting is correct.
			log.Println("No deals formatted into embeds, though new deals were identified.")
		}

		// Update Firestore with the timestamp of the most recent deal processed in this batch
		// This happens even if Discord sending failed, to prevent reprocessing.
		log.Printf("Updating Firestore with the latest processed timestamp: %s", latestNewDealTimestamp.Format(time.RFC3339))
		if err := writeLastProcessedTimestamp(ctx, fsClient, latestNewDealTimestamp); err != nil { // Assumes writeLastProcessedTimestamp is defined
			log.Printf("Error writing last processed timestamp to Firestore: %v", err)
			if handlerProcessingError != nil {
				handlerProcessingError = fmt.Errorf("multiple errors: %v; and failed to write timestamp: %w", handlerProcessingError, err)
			} else {
				handlerProcessingError = fmt.Errorf("failed to write timestamp: %w", err)
			}
		} else {
			log.Println("Successfully updated Firestore with the new latest processed timestamp.")
		}
	} else {
		log.Println("No new deals found since the last check.")
	}

	log.Printf("Finished processing. Identified %d new deals.\n", len(newDeals))

	if handlerProcessingError != nil {
		log.Printf("Overall error processing deals: %v", handlerProcessingError)
		http.Error(w, fmt.Sprintf("Failed to process deals due to one or more errors: %v", handlerProcessingError), http.StatusInternalServerError)
		return
	}

	fmt.Fprintln(w, "Deals processed successfully.")
	log.Println("Deals processed successfully.")
}

func main() {
	log.Println("Starting RFD Hot Deals Bot server...")

	http.HandleFunc("/", ProcessDealsHandler)

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
