package ai

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func BenchmarkPromptConstruction_Original(b *testing.B) {
	deal := &models.DealInfo{
		Title:         "Sample Deal Title",
		Description:   "This is a sample description for the deal.",
		Comments:      "User comments summary here.",
		Summary:       "RFD summary here.",
		ActualDealURL: "https://example.com/deal",
		Price:         "$99.99",
		OriginalPrice: "$149.99",
		Savings:       "$50.00",
		Retailer:      "ExampleRetailer",
	}
	link := deal.ActualDealURL

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var optionalFields string
		if deal.OriginalPrice != "" {
			optionalFields += fmt.Sprintf("Original Price: \"%s\"\n", deal.OriginalPrice)
		}
		if deal.Savings != "" {
			optionalFields += fmt.Sprintf("Savings: \"%s\"\n", deal.Savings)
		}

		_ = fmt.Sprintf(`
Analyze this deal:
Title: "%s"
Description: "%s"
User Comments Summary: "%s"
RFD Summary: "%s"
Deal Link: "%s"
Price: "%s"
%sRetailer: "%s"

Task:
1. Create a clean, concise title (5-15 words). Remove fluff ("Lava Hot", "Price Error"), store names if redundant, and focus on the product and price/discount.
2. Determine if this is "Lava Hot". Be extremely strict: only flag as True if you would genuinely FOMO or lose sleep over missing this deal. Regular sales should be False.

You MUST respond ONLY with a raw JSON object containing exactly two keys: "clean_title" (string) and "is_lava_hot" (boolean). Do not include any other text, markdown formatting, or backticks.
`, deal.Title, deal.Description, deal.Comments, deal.Summary, link, deal.Price, optionalFields, deal.Retailer)
	}
}

func BenchmarkPromptConstruction_Optimized(b *testing.B) {
	deal := &models.DealInfo{
		Title:         "Sample Deal Title",
		Description:   "This is a sample description for the deal.",
		Comments:      "User comments summary here.",
		Summary:       "RFD summary here.",
		ActualDealURL: "https://example.com/deal",
		Price:         "$99.99",
		OriginalPrice: "$149.99",
		Savings:       "$50.00",
		Retailer:      "ExampleRetailer",
	}
	link := deal.ActualDealURL

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var optionalFields strings.Builder
		if deal.OriginalPrice != "" {
			optionalFields.WriteString("Original Price: \"")
			optionalFields.WriteString(deal.OriginalPrice)
			optionalFields.WriteString("\"\n")
		}
		if deal.Savings != "" {
			optionalFields.WriteString("Savings: \"")
			optionalFields.WriteString(deal.Savings)
			optionalFields.WriteString("\"\n")
		}

		_ = fmt.Sprintf(`
Analyze this deal:
Title: "%s"
Description: "%s"
User Comments Summary: "%s"
RFD Summary: "%s"
Deal Link: "%s"
Price: "%s"
%sRetailer: "%s"

Task:
1. Create a clean, concise title (5-15 words). Remove fluff ("Lava Hot", "Price Error"), store names if redundant, and focus on the product and price/discount.
2. Determine if this is "Lava Hot". Be extremely strict: only flag as True if you would genuinely FOMO or lose sleep over missing this deal. Regular sales should be False.

You MUST respond ONLY with a raw JSON object containing exactly two keys: "clean_title" (string) and "is_lava_hot" (boolean). Do not include any other text, markdown formatting, or backticks.
`, deal.Title, deal.Description, deal.Comments, deal.Summary, link, deal.Price, optionalFields.String(), deal.Retailer)
	}
}
