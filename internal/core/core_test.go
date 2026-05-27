package core

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type mockStore struct {
	history map[string]*models.CorePriceHistory
	subs    []models.Subscription
	rules   []models.CoreRule
}

func (m *mockStore) GetCorePriceHistory(ctx context.Context, productName string) (*models.CorePriceHistory, bool, error) {
	h, ok := m.history[productName]
	if !ok {
		return nil, false, nil
	}
	return h, true, nil
}

func (m *mockStore) SaveCorePriceHistory(ctx context.Context, history models.CorePriceHistory) error {
	m.history[history.ProductName] = &history
	return nil
}

func (m *mockStore) GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	return m.subs, nil
}

func (m *mockStore) GetCoreRules(ctx context.Context) ([]models.CoreRule, error) {
	return m.rules, nil
}

type mockNotifier struct {
	sent []models.CoreDeal
}

func (m *mockNotifier) SendCoreDeal(ctx context.Context, deal models.CoreDeal, subs []models.Subscription) (map[string]string, error) {
	m.sent = append(m.sent, deal)
	return map[string]string{"123": "456"}, nil
}

func TestParseNotificationText(t *testing.T) {
	tests := []struct {
		text         string
		wantProduct  string
		wantStore    string
		wantPrice    float64
		wantCurrency string
		wantLink     string
		wantIsDeal   bool
	}{
		{
			text:         "$83.01 | Amazon COM | Pokemon TCG Scarlet & Violet 10.5 White Flare... \u2068@USA\u2069",
			wantProduct:  "Pokemon TCG Scarlet & Violet 10.5 White Flare",
			wantStore:    "Amazon COM",
			wantPrice:    83.01,
			wantCurrency: "USD",
			wantLink:     "",
			wantIsDeal:   true,
		},
		{
			text:         "C$545.99 | Amazon CA | TEAMGROUP DDR5... \u2068@Canada\u2069 https://amazon.ca/dp/123",
			wantProduct:  "TEAMGROUP DDR5",
			wantStore:    "Amazon CA",
			wantPrice:    545.99,
			wantCurrency: "CAD",
			wantLink:     "https://amazon.ca/dp/123",
			wantIsDeal:   true,
		},
		{
			text:         "\u20ac62.98 | Amazon DE | Destined Rivals... \u2068@Germany\u2069",
			wantProduct:  "Destined Rivals",
			wantStore:    "Amazon DE",
			wantPrice:    62.98,
			wantCurrency: "EUR",
			wantLink:     "",
			wantIsDeal:   true,
		},
		{
			text:         "\u00a31.30 | Waitrose | Essential Eggs \u2068@UK\u2069",
			wantProduct:  "Essential Eggs",
			wantStore:    "Waitrose",
			wantPrice:    1.30,
			wantCurrency: "GBP",
			wantLink:     "",
			wantIsDeal:   true,
		},
		{
			text:       "Invalid format",
			wantIsDeal: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			rates := NewRateManager()
			parsed, isDeal := ParseNotification(rates, tt.text)
			if isDeal != tt.wantIsDeal {
				t.Fatalf("isDeal = %v, want %v", isDeal, tt.wantIsDeal)
			}
			if isDeal {
				if parsed.ProductName != tt.wantProduct {
					t.Errorf("product = %q, want %q", parsed.ProductName, tt.wantProduct)
				}
				if parsed.StoreName != tt.wantStore {
					t.Errorf("store = %q, want %q", parsed.StoreName, tt.wantStore)
				}
				if parsed.Price != tt.wantPrice {
					t.Errorf("price = %v, want %v", parsed.Price, tt.wantPrice)
				}
				if parsed.Currency != tt.wantCurrency {
					t.Errorf("currency = %q, want %q", parsed.Currency, tt.wantCurrency)
				}
				if parsed.Link != tt.wantLink {
					t.Errorf("link = %q, want %q", parsed.Link, tt.wantLink)
				}
			}
		})
	}
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		name  string
		slice []float64
		pct   float64
		want  float64
	}{
		{
			name:  "single element",
			slice: []float64{100},
			pct:   25,
			want:  100,
		},
		{
			name:  "two elements",
			slice: []float64{100, 200},
			pct:   25,
			want:  125,
		},
		{
			name:  "three elements",
			slice: []float64{100, 150, 200},
			pct:   50,
			want:  150,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := percentile(tt.slice, tt.pct)
			if got != tt.want {
				t.Errorf("percentile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeProductName(t *testing.T) {
	rules := []models.CoreRule{
		{Pattern: `(?i)\b(\d+)g\b`, Replace: "${1}gb"},
		{Pattern: `(?i)\s*-\s*deal of the day!*`, Replace: ""},
		{Pattern: `(?i)\bamazon\b`, Replace: ""},
	}

	tests := []struct {
		name string
		want string
	}{
		{"Pokemon Twilight Masquerade Booster Box - Deal of the Day!", "pokemon twilight masquerade booster box"},
		{"Nvidia RTX 5060 Ti 8g", "nvidia rtx 5060 ti 8gb"},
		{"Amazon Pokemon TCG Scarlet & Violet 16g", "pokemon tcg scarlet & violet 16gb"},
	}

	for _, tt := range tests {
		got := NormalizeProductName(tt.name, rules)
		if got != tt.want {
			t.Errorf("NormalizeProductName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestProcessNotification(t *testing.T) {
	ctx := context.Background()

	store := &mockStore{
		history: make(map[string]*models.CorePriceHistory),
		subs: []models.Subscription{
			{GuildID: "g1", ChannelID: "c1", SubscriptionType: "core", DealType: "core_alerts"},
		},
	}
	notifier := &mockNotifier{}
	rates := NewRateManager()
	p := NewProcessor(store, notifier, rates)

	prices := []float64{100, 150, 140, 130, 120, 110, 160, 170, 180, 190}
	for i, price := range prices {
		msg := fmt.Sprintf("$%.2f | Amazon US | Test Product @USA", price)
		err := p.ProcessNotification(ctx, "Title", msg, nil, fmt.Sprintf("ev%d", i+1), "com.discord")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if len(notifier.sent) != 0 {
		t.Errorf("expected no notifications before 10 prior observations, got %d", len(notifier.sent))
	}

	h, ok := store.history["test product"]
	if !ok || len(h.Prices) != 10 || math.Abs(h.Prices[0]-136.986) > 0.1 {
		t.Errorf("expected price history to be saved with 10 observations, got: %+v", h)
	}

	msg := "$80.00 | Amazon US | Test Product @USA"
	err := p.ProcessNotification(ctx, "Title", msg, nil, "ev-low", "com.discord")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.sent) != 1 {
		t.Errorf("expected 1 notification sent on price drop, got %d", len(notifier.sent))
	}

	sentDeal := notifier.sent[0]
	if sentDeal.ProductName != "Test Product" || sentDeal.StoreName != "Amazon US" || math.Abs(sentDeal.PriceCAD-109.589) > 0.1 {
		t.Errorf("unexpected sent deal details: %+v", sentDeal)
	}

	err = p.ProcessNotification(ctx, "Title", msg, nil, "ev-low", "com.discord")
	if err != nil {
		t.Fatalf("unexpected error on duplicate event: %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Errorf("expected duplicate event to be ignored, got %d notifications", len(notifier.sent))
	}
}
