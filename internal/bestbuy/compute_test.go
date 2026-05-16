package bestbuy

import (
	"context"
	"strings"
	"testing"
)

func TestParseComputeSpecWorkstation(t *testing.T) {
	product := Product{
		Name:      "Refurbished (Good)-Dell Precision 5820, Xeon W-2133, 6 Cores/ 12 Threads, 32GB RAM, 512GB NVMe, Nvidia Quadro P4000, Windows 11 Pro",
		SalePrice: 650,
		SellerID:  "seller-a",
		Source:    "seller:seller-a",
	}

	spec := ParseComputeSpec(product)
	if !spec.IsCompute {
		t.Fatalf("IsCompute = false, reason=%q spec=%#v", spec.RejectReason, spec)
	}
	if spec.Class != ComputeClassWorkstationDesktop {
		t.Fatalf("Class = %q, want workstation desktop", spec.Class)
	}
	if spec.Family != "dell_precision" || spec.Model != "5820" {
		t.Fatalf("family/model = %q/%q, want dell_precision/5820", spec.Family, spec.Model)
	}
	if spec.RAMGB != 32 || spec.CoreCount != 6 {
		t.Fatalf("RAM/Core = %.0f/%d, want 32/6", spec.RAMGB, spec.CoreCount)
	}
	if !strings.Contains(strings.ToLower(spec.GPU), "quadro p4000") {
		t.Fatalf("GPU = %q, want Quadro P4000", spec.GPU)
	}
}

func TestParseComputeSpecServerWithSpecs(t *testing.T) {
	product := Product{
		Name: "Refurbished (Excellent) - HP ProLiant DL380 Gen10 Rack Mount Server | 2U | Server | 2x Silver 4114 |192GB | 3x 1TB SSD",
		Specs: map[string]string{
			"custom0ramsize":        "192",
			"custom0processorcores": "10",
			"custom0processortype":  "2 x Silver 4114",
		},
	}

	spec := ParseComputeSpec(product)
	if !spec.IsCompute {
		t.Fatalf("IsCompute = false, reason=%q spec=%#v", spec.RejectReason, spec)
	}
	if spec.Class != ComputeClassRackServer {
		t.Fatalf("Class = %q, want rack server", spec.Class)
	}
	if spec.Family != "hpe_proliant" || spec.Model != "dl380" || spec.Generation != "gen10" {
		t.Fatalf("family/model/generation = %q/%q/%q", spec.Family, spec.Model, spec.Generation)
	}
	if spec.RAMGB != 192 || spec.SSDTB != 3 {
		t.Fatalf("RAM/SSD = %.0f/%.1f, want 192/3.0", spec.RAMGB, spec.SSDTB)
	}
}

func TestParseComputeSpecRejectsAccessories(t *testing.T) {
	products := []Product{
		{Name: `HPE Easy Install Rail 1 Kit (P52349B21)`},
		{Name: `High-Performance 750W Dell PowerEdge R630 R730 Power Supply`},
		{Name: `OWC 64GB DDR4 Server ECC Registered RDIMM Memory RAM Compatible with Lenovo ThinkSystem`},
		{Name: `Dell PowerEdge R730 3.5" Drive Tray Caddy`},
		{Name: `HP ZBook 150W AC Adapter Power Cord`},
		{Name: `240W 180W AC Charger Fit for Dell Precision 7760 Mobile Workstation`},
		{Name: `MSI Gaming Mouse 26000 DPI Optical Sensor`},
		{Name: `Dell Precision 7730 Replacement LCD Screen`},
		{Name: `Intel Xeon E3-1220 V3 Quad-core (4 Core) 3.10 Ghz Processor`, CategoryName: "CPU / Computer Processors"},
	}
	for _, product := range products {
		spec := ParseComputeSpec(product)
		if spec.IsCompute {
			t.Fatalf("%q parsed as compute: %#v", product.Name, spec)
		}
		if spec.RejectReason == "" {
			t.Fatalf("%q missing reject reason: %#v", product.Name, spec)
		}
	}
}

func TestParseComputeSpecRejectsConsumerLaptops(t *testing.T) {
	products := []Product{
		{
			Name:      `Dell Chromebook 3120 32GB SSD - 96GB RAM`,
			SalePrice: 89,
		},
	}
	for _, product := range products {
		spec := ParseComputeSpec(product)
		if spec.IsCompute {
			t.Fatalf("%q parsed as compute: %#v", product.Name, spec)
		}
	}
}

func TestParseComputeSpecIncludesHighComputeLaptopsAndApple(t *testing.T) {
	products := []Product{
		{
			Name: "Lenovo IdeaPad Laptop Intel Core i7 64GB RAM 1TB SSD",
		},
		{
			Name: "Apple Mac Studio M2 Ultra 128GB RAM 2TB SSD",
		},
		{
			Name: "Apple MacBook Pro M4 Max 48GB RAM 1TB SSD",
		},
		{
			Name: "Snapdragon X Elite Laptop 32GB RAM 1TB SSD",
		},
	}
	for _, product := range products {
		spec := ParseComputeSpec(product)
		if !spec.IsCompute {
			t.Fatalf("%q parsed as not compute: %#v", product.Name, spec)
		}
	}
}

func TestParseComputeSpecTitleWinsOverDetails(t *testing.T) {
	product := Product{
		Name: "Refurbished (Good) - HP Z640 Workstation | E5-2680 V3 12 Core | 32GB|256GB SSD+500GB HDD | K2000 | WIN 10P",
		Specs: map[string]string{
			"custom0ramsize":        "64",
			"custom0processorcores": "20",
			"custom0graphics":       "NVIDIA Quadro 4000",
		},
	}

	spec := ParseComputeSpec(product)
	if spec.RAMGB != 32 {
		t.Fatalf("RAMGB = %.0f, want title RAM 32 over details", spec.RAMGB)
	}
	if spec.CoreCount != 12 {
		t.Fatalf("CoreCount = %d, want title core count 12 over details", spec.CoreCount)
	}
	if !strings.Contains(strings.ToLower(spec.GPU), "k2000") {
		t.Fatalf("GPU = %q, want title GPU K2000 over details Quadro 4000", spec.GPU)
	}
}

func TestParseComputeSpecRejectsGenericLowComputeDesktop(t *testing.T) {
	product := Product{
		Name: "Refurbished (Excellent) Dell Optiplex 7070 Micro Tower Desktop | Core i5-8500 - 512GB SSD Hard Drive - 32GB RAM | 6 cores @ 4.1 GHz Win 11 Pro Black",
	}
	spec := ParseComputeSpec(product)
	if spec.IsCompute {
		t.Fatalf("generic 32GB office desktop parsed as compute: %#v", spec)
	}
}

func TestScoreComputeOutlier(t *testing.T) {
	candidate := Product{
		SKU:        "candidate",
		Name:       "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro P4000",
		SalePrice:  650,
		SellerID:   "seller-a",
		SellerName: "Seller A",
		Source:     "seller:seller-a",
	}
	spec := ParseComputeSpec(candidate)
	comps := []ComputeObservation{
		computeComp("c1", "seller-b", 2500),
		computeComp("c2", "seller-c", 2600),
		computeComp("c3", "seller-d", 2700),
		computeComp("c4", "seller-e", 2800),
		computeComp("c5", "seller-f", 2900),
	}

	score := ScoreComputeOutlier(candidate, spec, comps)
	if !score.IsWarm {
		t.Fatalf("IsWarm = false, score=%#v", score)
	}
	if score.IsLavaHot {
		t.Fatalf("IsLavaHot = true, want warm-only score=%#v", score)
	}
	if score.ComparableCount != 5 {
		t.Fatalf("ComparableCount = %d, want 5", score.ComparableCount)
	}
	if score.GapPct < 70 {
		t.Fatalf("GapPct = %.2f, want 70+ pct", score.GapPct)
	}
}

func TestScoreComputeOutlierRequiresHugeGap(t *testing.T) {
	candidate := Product{
		SKU:        "candidate",
		Name:       "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro P4000",
		SalePrice:  650,
		SellerID:   "seller-a",
		SellerName: "Seller A",
		Source:     "seller:seller-a",
	}
	spec := ParseComputeSpec(candidate)
	comps := []ComputeObservation{
		computeComp("c1", "seller-b", 1100),
		computeComp("c2", "seller-c", 1200),
		computeComp("c3", "seller-d", 1250),
		computeComp("c4", "seller-e", 1300),
		computeComp("c5", "seller-f", 1350),
	}

	score := ScoreComputeOutlier(candidate, spec, comps)
	if score.IsWarm {
		t.Fatalf("IsWarm = true for merely-good comp gap: %#v", score)
	}
}

func TestScoreComputeOutlierIgnoresSameSeller(t *testing.T) {
	candidate := Product{
		SKU:        "candidate",
		Name:       "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro P4000",
		SalePrice:  650,
		SellerID:   "seller-a",
		SellerName: "Seller A",
		Source:     "seller:seller-a",
	}
	spec := ParseComputeSpec(candidate)
	comps := []ComputeObservation{
		computeComp("c1", "seller-a", 1200),
		computeComp("c2", "seller-b", 1200),
		computeComp("c3", "seller-c", 1250),
	}

	score := ScoreComputeOutlier(candidate, spec, comps)
	if score.ComparableCount != 2 {
		t.Fatalf("ComparableCount = %d, want same seller excluded", score.ComparableCount)
	}
	if score.IsWarm {
		t.Fatalf("IsWarm = true with too few independent comps: %#v", score)
	}
}

func TestScoreComputeOutlierAllowsSameSKUDifferentSeller(t *testing.T) {
	candidate := Product{
		SKU:        "same-sku",
		Name:       "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro P4000",
		SalePrice:  650,
		SellerID:   "seller-a",
		SellerName: "Seller A",
		Source:     "seller:seller-a",
	}
	spec := ParseComputeSpec(candidate)
	comps := []ComputeObservation{
		computeComp("same-sku", "seller-b", 2500),
		computeComp("same-sku", "seller-c", 2600),
		computeComp("same-sku", "seller-d", 2700),
		computeComp("same-sku", "seller-e", 2800),
		computeComp("same-sku", "seller-f", 2900),
	}

	score := ScoreComputeOutlier(candidate, spec, comps)
	if score.ComparableCount != 5 {
		t.Fatalf("ComparableCount = %d, want same SKU from different sellers included", score.ComparableCount)
	}
}

func TestScoreComputeOutlierUsesStoredEbaySoldComparables(t *testing.T) {
	candidate := Product{
		SKU:       "extreme",
		Name:      "Dell PowerEdge R740 768GB RAM 24 Core Xeon Server",
		SalePrice: 1,
		SellerID:  "seller-a",
		Source:    "seller:seller-a",
	}
	observation := ComputeObservation{Product: candidate, Spec: ParseComputeSpec(candidate)}
	comps := []ComputeObservation{
		{
			Product: Product{SKU: "prior", Name: "Prior PowerEdge", SalePrice: 1000, SellerID: "seller-b", Source: "seller:seller-b"},
			Spec:    ParseComputeSpec(Product{Name: "Dell PowerEdge R740 256GB RAM 24 Core Xeon Server"}),
			EbaySoldComparables: []ComputeExternalComparable{
				{Title: "Dell PowerEdge R740 256GB RAM 24 Core Xeon Server", Price: 1800, Source: "ebay-sold"},
				{Title: "Dell PowerEdge R740 384GB RAM 24 Core Xeon Server", Price: 2200, Source: "ebay-sold"},
				{Title: "Dell PowerEdge R740 512GB RAM 24 Core Xeon Server", Price: 2600, Source: "ebay-sold"},
				{Title: "Dell PowerEdge R740 768GB RAM 24 Core Xeon Server", Price: 3000, Source: "ebay-sold"},
				{Title: "Dell PowerEdge R740 1TB RAM 24 Core Xeon Server", Price: 3400, Source: "ebay-sold"},
			},
		},
	}

	score := ScoreComputeObservationOutlier(observation, comps)
	if !score.IsWarm {
		t.Fatalf("IsWarm = false using stored eBay sold comps: %#v", score)
	}
	if score.ComparableCount != 5 {
		t.Fatalf("ComparableCount = %d, want 5 stored eBay sold comps", score.ComparableCount)
	}
}

func TestScoreComputeOutlierUsesEmbeddingCluster(t *testing.T) {
	candidate := Product{
		SKU:        "candidate",
		Name:       "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro P4000",
		SalePrice:  650,
		SellerID:   "seller-a",
		SellerName: "Seller A",
		Source:     "seller:seller-a",
	}
	observation := ComputeObservation{
		Product:         candidate,
		Spec:            ParseComputeSpec(candidate),
		EmbeddingVector: []float64{1, 0},
	}
	comps := []ComputeObservation{
		computeCompWithVector("near-1", "seller-b", 700, []float64{1, 0}),
		computeCompWithVector("near-2", "seller-c", 720, []float64{0.98, 0.02}),
		computeCompWithVector("near-3", "seller-d", 740, []float64{0.97, 0.03}),
		computeCompWithVector("far-1", "seller-e", 2600, []float64{0, 1}),
		computeCompWithVector("far-2", "seller-f", 2800, []float64{0.05, 0.95}),
		computeCompWithVector("far-3", "seller-g", 3000, []float64{0.10, 0.90}),
	}

	score := ScoreComputeObservationOutlier(observation, comps)
	if score.ComparableCount != 3 {
		t.Fatalf("ComparableCount = %d, want nearest embedding cluster only", score.ComparableCount)
	}
	if score.IsWarm {
		t.Fatalf("IsWarm = true; far-away high-price comps should not make a deal: %#v", score)
	}
	if score.MedianPrice > 750 {
		t.Fatalf("MedianPrice = %.2f, want near-cluster median", score.MedianPrice)
	}
}

func TestHashComputeEmbedderReturnsNormalizedVectors(t *testing.T) {
	model, vectors, err := NewComputeEmbedder("").Embed(context.Background(), []string{
		"class:workstation; family:dell_precision; ram_gb:32",
		"class:rack_server; family:hpe_proliant; ram_gb:192",
	})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if model != "local-token-hash-v1" {
		t.Fatalf("model = %q", model)
	}
	if len(vectors) != 2 || len(vectors[0]) != 128 {
		t.Fatalf("vector shape = %d/%d", len(vectors), len(vectors[0]))
	}
}

func computeComp(sku, sellerID string, price float64) ComputeObservation {
	product := Product{
		SKU:       sku,
		Name:      "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro P4000",
		SalePrice: price,
		SellerID:  sellerID,
		Source:    "seller:" + sellerID,
	}
	return ComputeObservation{Product: product, Spec: ParseComputeSpec(product)}
}

func computeCompWithVector(sku, sellerID string, price float64, vector []float64) ComputeObservation {
	observation := computeComp(sku, sellerID, price)
	observation.EmbeddingVector = vector
	return observation
}
