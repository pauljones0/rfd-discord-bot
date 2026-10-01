package scraper

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
)

func TestHTMLSizeLimitRejectsTruncationIncludingChunkedResponses(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked=%t", chunked), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if chunked {
					w.(http.Flusher).Flush()
				}
				fmt.Fprint(w, strings.Repeat(" ", 5<<20)+"<h1>Must not parse a truncated page</h1>")
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			client := New(&config.Config{AllowedDomains: []string{target.Hostname()}}, DefaultSelectors())
			if doc, err := client.fetchHTMLContent(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), "exceeds") || doc != nil {
				t.Fatalf("oversized page accepted: %v", err)
			}
		})
	}
}
