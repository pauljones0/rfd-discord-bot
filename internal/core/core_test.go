package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type mockStore struct {
	history  map[string]*models.CorePriceHistory
	catStats map[string]*models.CoreCategoryStats
	subs     []models.Subscription
	rules    []models.CoreRule
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

func (m *mockStore) GetCoreCategoryStats(ctx context.Context, category string) (*models.CoreCategoryStats, bool, error) {
	s, ok := m.catStats[category]
	if !ok {
		return nil, false, nil
	}
	return s, true, nil
}

func (m *mockStore) SaveCoreCategoryStats(ctx context.Context, stats models.CoreCategoryStats) error {
	m.catStats[stats.Category] = &stats
	return nil
}

func (m *mockStore) GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	return m.subs, nil
}

func (m *mockStore) GetCoreRules(ctx context.Context) ([]models.CoreRule, error) {
	return m.rules, nil
}

func (m *mockStore) WipeCorePriceHistory(ctx context.Context) error {
	m.history = make(map[string]*models.CorePriceHistory)
	return nil
}

type mockNotifier struct {
	sent       []models.CoreDeal
	systemSent []models.CoreSystemAlert
}

func (m *mockNotifier) SendCoreAlert(ctx context.Context, alert models.CoreAlert, subs []models.Subscription) (map[string]string, error) {
	m.sent = append(m.sent, alert.Deal)
	return map[string]string{"123": "456"}, nil
}

func (m *mockNotifier) UpdateCoreAlert(ctx context.Context, alert models.CoreAlert) error {
	return nil
}

func (m *mockNotifier) SendCoreSystemAlert(ctx context.Context, alert models.CoreSystemAlert, subs []models.Subscription) error {
	m.systemSent = append(m.systemSent, alert)
	return nil
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
			text:         "362,36 € | Proshop | PNY GeForce RTX 5060 Dual Fan OC - 8GB GDDR7 RAM - Grafikkarte - VCG50608DFXPB1-O ⁨@Germany⁩",
			wantProduct:  "PNY GeForce RTX 5060 Dual Fan OC - 8GB GDDR7 RAM - Grafikkarte - VCG50608DFXPB1-O",
			wantStore:    "Proshop",
			wantPrice:    362.36,
			wantCurrency: "EUR",
			wantLink:     "",
			wantIsDeal:   true,
		},
		{
			text:         "1.652,00 € | Proshop | Inno3D GeForce RTX 5080 iCHILL X3 - 16GB GDDR7 RAM - Grafikkarte - C50803-16D7X-176069R ⁨@Germany⁩",
			wantProduct:  "Inno3D GeForce RTX 5080 iCHILL X3 - 16GB GDDR7 RAM - Grafikkarte - C50803-16D7X-176069R",
			wantStore:    "Proshop",
			wantPrice:    1652.00,
			wantCurrency: "EUR",
			wantLink:     "",
			wantIsDeal:   true,
		},
		{
			text:         "1 585,00 zł | Proshop | PNY GeForce RTX 5060 Dual Fan OC - 8GB GDDR7 RAM - Karta graficzna - VCG50608DFXPB1-O ⁨@Poland⁩",
			wantProduct:  "PNY GeForce RTX 5060 Dual Fan OC - 8GB GDDR7 RAM - Karta graficzna - VCG50608DFXPB1-O",
			wantStore:    "Proshop",
			wantPrice:    1585.00,
			wantCurrency: "PLN",
			wantLink:     "",
			wantIsDeal:   true,
		},
		{
			text:         "US$1,652.00 | Example Store | GPU ⁨@USA⁩",
			wantProduct:  "GPU",
			wantStore:    "Example Store",
			wantPrice:    1652.00,
			wantCurrency: "USD",
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
		name     string
		category string
		want     string
	}{
		{"Pokemon Twilight Masquerade Booster Box - Deal of the Day!", "pokemon", "tcg twilight masquerade booster box"},
		{"Nvidia RTX 5060 Ti 8g", "rtx5060ti", "gpu rtx 5060 ti 8gb"},
		{"ASUS GeForce RTX 5060 Ti Dual 8GB OC Gaming Graphics Card", "rtx5060ti", "gpu rtx 5060 ti 8gb"},
		{"Gigabyte Geforce Rtx 5060 Ti Aero Oc 8Gd Gddr7", "rtx5060ti", "gpu rtx 5060 ti 8gb"},
		{"ASUS PRIME GeForce RTX5060TI O8G OC", "rtx5060ti", "gpu rtx 5060 ti 8gb"},
		{"ASUS The SFF-Ready Prime GeForce RTX™ 5060 Ti 16GB", "rtx5060ti", "gpu rtx 5060 ti 16gb"},
		{"ASUS Dual NVIDIA GeForce RTX 5060 Ti OC Edition", "rtx5060ti", "gpu rtx 5060 ti unknown-vram"},
		{"MSI GeForce RTX 5070 Ti 16G Shadow 3X OC", "rtx5070ti", "gpu rtx 5070 ti 16gb"},
		{"PNY GeForce RTX™ 5070 Ti Overclocked Triple Fan", "rtx5070ti", "gpu rtx 5070 ti 16gb"},
		{"ASUS Dual NVIDIA GeForce RTX 5060 8GB GDDR7 RAM", "rtx5060", "gpu rtx 5060 8gb"},
		{"ASUS PRIME NVIDIA GeForce RTX 5070 OC Edition 12GB", "rtx5070", "gpu rtx 5070 12gb"},
		{"NVIDIA GeForce RTX 5060 Ti 32GB DDR5 RAM", "rtx5060ti", "gpu rtx 5060 ti unknown-vram"},
		{"NVIDIA GeForce RTX 5080 4GB DDR5 RAM", "rtx5080", "gpu rtx 5080 16gb"},
		{"NVIDIA GeForce RTX 5090 4GB DDR5 RAM", "rtx5090", "gpu rtx 5090 32gb"},
		{"NVIDIA GeForce RTX 4090 32GB DDR5 RAM", "nvidia-rtx-deals", "gpu rtx 4090 24gb"},
		{"ASUS Dual Radeon RX 9060 XT 8GB GDDR6 PCIe", "rx-9060xt", "gpu rx 9060 xt 8gb"},
		{"ASUS Dual Radeon RX9060XT 16G GDDR6 Gaming", "rx-9060xt", "gpu rx 9060 xt 16gb"},
		{"PowerColor RX 9060XT Reaper DDR6 Retail 8.192 MB", "rx-9060xt", "gpu rx 9060 xt 8gb"},
		{"PowerColor RX 9060XT Reaper Retail 16.384 MB", "rx-9060xt", "gpu rx 9060 xt 16gb"},
		{"ASRock AMD Radeon RX 9060 XT Challenger", "rx-9060xt", "gpu rx 9060 xt unknown-vram"},
		{"Sapphire Technology Nitro Plus AMD Radeon RX", "rx-9060xt", "gpu rx 9060 xt unknown-vram"},
		{"Sapphire 11350-04-20G Pulse AMD Radeon RX 9060...", "rx-9060xt", "gpu rx 9060 xt unknown-vram"},
		{"ASUS Prime Radeon RX 9070 OC Edition 16GB GDDR6", "rx-9070", "gpu rx 9070 16gb"},
		{"Gigabyte GV R9070GAMING OC 16GD", "rx-9070", "gpu rx 9070 16gb"},
		{"ASUS Prime Radeon RX 9070 XT OC Edition 16GB GDDR6", "rx-9070xt", "gpu rx 9070 xt 16gb"},
		{"ASUS Prime Radeon RX 9070 XT OC Edition 32GB DDR5 RAM", "rx-9070xt", "gpu rx 9070 xt 16gb"},
		{"Gaming PC NVIDIA GeForce RTX 5070 16GB DDR5 RAM", "ddr5-deals", "ram 16gb"},
		{"Amazon Pokemon TCG Scarlet & Violet 16g", "pokemon", "tcg scarlet & violet 16gb"},
		{"Crucial Pro RAM DDR5 64Go Kit (2x32Go) 6000MHz", "64gb", "ram 64gb 2x32gb 6000"},
		{"CORSAIR VENGEANCE DDR5 RAM 64GB (2x32GB) Bis zu", "64gb", "ram 64gb 2x32gb"},
		{"CORSAIR Vengeance 16 GB RGB DDR5 6000 MHz PC RAM - 8 GB x 2", "16gb", "ram 16gb 2x8gb 6000"},
		{"Patriot Memory Viper Venom Kit DDR5 RAM 16Go (2", "16gb", "ram 16gb"},
		{"Crucial Pro Overclocking Edition Module DDR5 16...", "16gb", "ram 16gb"},
		{"Crucial Pro Overclocking Edition Module DDR5 32 ...", "32gb", "ram 32gb"},
		{"TEAMGROUP Elite TED532G4800C40DC01 Lot de 2", "unknown-ddr5", "ram 32gb 2x16gb"},
		{"Kingston FURY Beast KF560C36BBE-8 Module", "unknown-ddr5", "ram 8gb"},
		{"Kingston FURY Beast KF560C36BBEAK2-32", "unknown-ddr5", "ram 32gb 2x16gb"},
		{"G Skill F5-5200J3636D32GX2-FX5 Memor", "unknown-ddr5", "ram 64gb 2x32gb"},
		{"G.skill Trident Z5 Royal Neo - Ddr5 - Kit", "unknown-ddr5", "ram unknown g.skill trident z5 royal neo ddr5 kit"},
		{"Magic: The Gathering | Avatar: The Last Airbender Booster Box", "magic-the-gathering", "tcg avatar the last airbender booster box"},
		{"Samsung 980 Pro 2TB NVMe SSD Gen4", "Core Deal", "storage samsung 980 pro 2tb"},
		{"WD Black SN850X 1TB SSD", "Core Deal", "storage wd sn850x 1tb"},
		{"Crucial MX500 4TB SATA SSD", "Core Deal", "storage crucial mx500 4tb"},
		{"Seagate BarraCuda 8TB Hard Drive", "Core Deal", "storage seagate 8tb"},
	}

	for _, tt := range tests {
		got := NormalizeProductName(tt.name, rules, tt.category)
		if got != tt.want {
			t.Errorf("NormalizeProductName(%q, %q) = %q, want %q", tt.name, tt.category, got, tt.want)
		}
	}
}

func TestIsAmbiguous(t *testing.T) {
	tests := []struct {
		name      string
		truncated bool
		want      bool
	}{
		{"Monitor", false, true},
		{"Samsung Monitor", false, false},
		{"ram 32gb 2x16gb", true, false},
		{"tcg avatar booster box", true, false},
		{"Pokemon", false, true},
	}

	for _, tt := range tests {
		got := isAmbiguous(tt.name, tt.truncated)
		if got != tt.want {
			t.Errorf("isAmbiguous(%q, %v) = %v, want %v", tt.name, tt.truncated, got, tt.want)
		}
	}
}

func TestProcessNotification(t *testing.T) {
	ctx := context.Background()

	store := &mockStore{
		history:  make(map[string]*models.CorePriceHistory),
		catStats: make(map[string]*models.CoreCategoryStats),
		subs: []models.Subscription{
			{GuildID: "g1", ChannelID: "c1", SubscriptionType: "core", DealType: "core_alerts"},
		},
	}
	notifier := &mockNotifier{}
	rates := NewRateManager()
	p := NewProcessor(store, notifier, rates)

	// Need to warm up category first
	for i := 0; i < 10; i++ {
		msg := fmt.Sprintf("$100 | Store | Cat Warmup %d @USA", i)
		p.ProcessNotification(ctx, "CoreFinder #test: CoreFinder", msg, nil, fmt.Sprintf("warm%d", i), "com.discord", "", "", "")
	}

	prices := []float64{100, 150, 140, 130, 120, 110, 160, 170, 180, 190}
	for i, price := range prices {
		msg := fmt.Sprintf("$%.2f | Amazon US | Test Product @USA", price)
		err := p.ProcessNotification(ctx, "CoreFinder #test: CoreFinder", msg, nil, fmt.Sprintf("ev%d", i+1), "com.discord", "", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if len(notifier.sent) != 0 {
		t.Errorf("expected no notifications before 10 prior observations, got %d", len(notifier.sent))
	}

	h, ok := store.history["test product"]
	if !ok || len(h.Prices) != 10 {
		t.Errorf("expected price history to be saved with 10 observations, got: %+v", h)
	}

	msg := "$20.00 | Amazon US | Test Product @USA"
	err := p.ProcessNotification(ctx, "CoreFinder #test: CoreFinder", msg, nil, "ev-low", "com.discord", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.sent) != 1 {
		t.Errorf("expected 1 notification sent on price drop, got %d", len(notifier.sent))
	}
}

func TestCoreAlertUsesLifetimeObservationCountPastRetainedCap(t *testing.T) {
	ctx := context.Background()
	prices := make([]float64, maxPriceHistoryEntries)
	for i := range prices {
		prices[i] = 100
	}

	priorObservationCount := maxPriceHistoryEntries + 150
	store := &mockStore{
		history: map[string]*models.CorePriceHistory{
			"test product": {
				ProductName:      "test product",
				Category:         "test",
				Prices:           prices,
				ObservationCount: priorObservationCount,
			},
		},
		catStats: map[string]*models.CoreCategoryStats{
			"test": {Category: "test", TotalCount: priorObservationCount},
		},
		subs: []models.Subscription{
			{GuildID: "g1", ChannelID: "c1", SubscriptionType: "core", DealType: "core_alerts"},
		},
	}
	notifier := &mockNotifier{}
	p := NewProcessor(store, notifier, NewRateManager())

	err := p.ProcessNotification(ctx, "CoreFinder #test: CoreFinder", "$20.00 | Amazon US | Test Product @USA", nil, "ev-low", "com.discord", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 notification sent on price drop, got %d", len(notifier.sent))
	}
	wantTotal := priorObservationCount + 1
	if notifier.sent[0].HistoryCount != wantTotal || notifier.sent[0].PriceSampleCount != maxPriceHistoryEntries {
		t.Fatalf("alert counts = total %d/sample %d, want %d/%d", notifier.sent[0].HistoryCount, notifier.sent[0].PriceSampleCount, wantTotal, maxPriceHistoryEntries)
	}
	history := store.history["test product"]
	if history.ObservationCount != wantTotal || len(history.Prices) != maxPriceHistoryEntries {
		t.Fatalf("stored history counts = total %d/sample %d, want %d/%d", history.ObservationCount, len(history.Prices), wantTotal, maxPriceHistoryEntries)
	}
}

func TestCategoryThreshold(t *testing.T) {
	ctx := context.Background()

	store := &mockStore{
		history:  make(map[string]*models.CorePriceHistory),
		catStats: make(map[string]*models.CoreCategoryStats),
		subs: []models.Subscription{
			{GuildID: "g1", ChannelID: "c1", SubscriptionType: "core", DealType: "core_alerts"},
		},
	}
	notifier := &mockNotifier{}
	rates := NewRateManager()
	p := NewProcessor(store, notifier, rates)

	// Give the product 10 observations, but the category 0
	normName := "test product"
	history := &models.CorePriceHistory{
		ProductName: normName,
		Category:    "newcat",
		Prices:      []float64{100, 100, 100, 100, 100, 100, 100, 100, 100, 100},
	}
	store.history[normName] = history

	// This should be a drop, but suppressed by category threshold
	msg := "$50.00 | Store | Test Product @USA"
	err := p.ProcessNotification(ctx, "CoreFinder #newcat: CoreFinder", msg, nil, "ev1", "com.discord", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.sent) != 0 {
		t.Errorf("expected notification suppressed by category threshold, got %d", len(notifier.sent))
	}

	// Warm up category
	for i := 0; i < 9; i++ { // we already have 1 from the suppressed call
		msg := fmt.Sprintf("$100 | Store | Cat Warmup %d @USA", i)
		p.ProcessNotification(ctx, "CoreFinder #newcat: CoreFinder", msg, nil, fmt.Sprintf("warm%d", i), "com.discord", "", "", "")
	}

	// Now it should fire with a slightly different price to avoid duplicate detection
	msg = "$40.00 | Store | Test Product @USA"
	err = p.ProcessNotification(ctx, "CoreFinder #newcat: CoreFinder", msg, nil, "ev2", "com.discord", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.sent) != 1 {
		t.Errorf("expected notification after category warmup, got %d", len(notifier.sent))
	}
}

func TestProcessNotificationBatchDoesNotDuplicateStructuredMessage(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{
		history: map[string]*models.CorePriceHistory{
			"test product": {
				ProductName: "test product",
				Category:    "test",
				Prices:      []float64{100, 100, 100, 100, 100, 100, 100, 100, 100, 100},
			},
		},
		catStats: map[string]*models.CoreCategoryStats{
			"test": {Category: "test", TotalCount: 10},
		},
		subs: []models.Subscription{
			{GuildID: "g1", ChannelID: "c1", SubscriptionType: "core", DealType: "core_alerts"},
		},
	}
	notifier := &mockNotifier{}
	p := NewProcessor(store, notifier, NewRateManager())

	msg := "$20.00 | Amazon US | Test Product @USA"
	err := p.ProcessNotificationBatch(ctx, DiscordNotificationBatch{
		ConversationTitle: "CoreFinder #test: CoreFinder",
		TickerText:        msg,
		Lines:             []string{msg},
		Messages: []DiscordNotificationMsg{
			{Text: msg},
		},
		EventID:       "batch-event",
		SourcePackage: "com.discord",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected one alert from structured message, got %d", len(notifier.sent))
	}
	if notifier.sent[0].EventID != "batch-event-0" {
		t.Fatalf("alert event ID = %q, want batch-event-0", notifier.sent[0].EventID)
	}
}

func TestCorePriceErrorOnlySubscriptionRequiresMeaningfulATL(t *testing.T) {
	ctx := context.Background()
	store := &mockStore{
		history: map[string]*models.CorePriceHistory{
			"test product": {
				ProductName: "test product",
				Category:    "test",
				Prices:      []float64{100, 180, 180, 180, 180, 180, 180, 180, 180, 180},
			},
		},
		catStats: map[string]*models.CoreCategoryStats{
			"test": {Category: "test", TotalCount: 10},
		},
		subs: []models.Subscription{
			{GuildID: "g1", ChannelID: "c1", SubscriptionType: "core", DealType: "core_price_errors"},
		},
	}
	notifier := &mockNotifier{}
	p := NewProcessor(store, notifier, NewRateManager())

	err := p.ProcessNotification(ctx, "CoreFinder #test: CoreFinder", "C$95.00 | Amazon CA | Test Product @Canada", nil, "not-meaningful-atl", "com.discord", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("expected price error above 10%% under ATL to be filtered, got %d alerts", len(notifier.sent))
	}

	err = p.ProcessNotification(ctx, "CoreFinder #test: CoreFinder", "C$80.00 | Amazon CA | Test Product @Canada", nil, "price-error", "com.discord", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected price error to notify price-error-only subscription, got %d alerts", len(notifier.sent))
	}
	if notifier.sent[0].AnomalyType != "Price Error / Used" {
		t.Fatalf("anomaly type = %q, want Price Error / Used", notifier.sent[0].AnomalyType)
	}
}

func TestGPUBucketSuppressesRecentNearDuplicateAlert(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := &mockStore{
		history: map[string]*models.CorePriceHistory{
			"gpu rtx 5070 12gb": {
				ProductName: "gpu rtx 5070 12gb",
				Category:    "rtx5070",
				Prices:      []float64{850, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000},
				RecentAlerts: []models.CoreAlert{
					{PriceCAD: 850, FiredAt: now.Add(-time.Hour)},
				},
			},
		},
		catStats: map[string]*models.CoreCategoryStats{
			"rtx5070": {Category: "rtx5070", TotalCount: 10},
		},
		subs: []models.Subscription{
			{GuildID: "g1", ChannelID: "c1", SubscriptionType: "core", DealType: "core_alerts"},
		},
	}
	notifier := &mockNotifier{}
	p := NewProcessor(store, notifier, NewRateManager())

	err := p.ProcessNotification(ctx, "CoreFinder #rtx5070: CoreFinder", "C$825.00 | Amazon CA | PNY GeForce RTX 5070 Overclocked Triple Fan @Canada", nil, "gpu-near-duplicate", "com.discord", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("expected repeated 5070 bucket alert to be suppressed, got %d alerts", len(notifier.sent))
	}

	err = p.ProcessNotification(ctx, "CoreFinder #rtx5070: CoreFinder", "C$700.00 | Amazon CA | MSI GeForce RTX 5070 Ventus 2X OC @Canada", nil, "gpu-meaningfully-lower", "com.discord", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected meaningfully lower 5070 bucket alert to send, got %d alerts", len(notifier.sent))
	}
	if notifier.sent[0].Category != "rtx5070" {
		t.Fatalf("category = %q, want rtx5070", notifier.sent[0].Category)
	}
}
