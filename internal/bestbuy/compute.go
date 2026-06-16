package bestbuy

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ComputeClassRackServer         = "rack_server"
	ComputeClassTowerServer        = "tower_server"
	ComputeClassWorkstationDesktop = "workstation_desktop"
	ComputeClassWorkstationLaptop  = "workstation_laptop"
	ComputeClassDesktop            = "desktop"
	ComputeClassLaptop             = "laptop"
	ComputeClassNAS                = "nas"
	ComputeClassComponent          = "component"
	ComputeClassAccessory          = "accessory"
	ComputeClassOther              = "other"

	defaultComputeWarmMinGapPct    = 70.0
	defaultComputeWarmMinGapAmount = 500.0
	defaultComputeHotMinGapPct     = 80.0
	defaultComputeHotMinGapAmount  = 1000.0

	computeMinComparableCount          = 5
	computeEmbeddingMinComparableCount = 3
	computeEmbeddingSimilarityCutoff   = 0.35
	computeEmbeddingMaxComparableCount = 60
)

var (
	ramPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*(?:gb|g)[\s|]+(?:ddr\d+[\s|]+)?(?:ecc\s+|registered\s+|rdimm\s+|sodimm\s+|lpddr\d+x?\s+)?ram\b`),
		regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*(?:gb|g)[\s|]+(?:ddr\d+[\s|]+)?memory\b`),
		regexp.MustCompile(`(?i)\bram\s*[:/]\s*(\d+(?:\.\d+)?)\s*(?:gb|g)\b`),
		regexp.MustCompile(`(?i)\|\s*(\d+(?:\.\d+)?)\s*(?:gb|g)\s*\|`),
		regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*(?:gb|g)[\s|]+(?:ddr\d+|lpddr\d+x?|ecc|rdimm)\b`),
	}
	storagePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:(\d+)\s*x\s*)?(\d+(?:\.\d+)?)\s*(tb|gb)[\s|]*(nvme|ssd|hdd|hard drive|sata|sas)`),
		regexp.MustCompile(`(?i)(?:(\d+)\s*x\s*)?(\d+(?:\.\d+)?)\s*(tb|gb)[\s|]+(?:pcie[\s|]+)?(?:4\.0[\s|]+)?(?:solid state|storage)`),
	}
	corePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(\d+)\s*(?:-| )?core\b`),
		regexp.MustCompile(`(?i)\b(\d+)\s*cores\b`),
	}
	cpuCountPattern = regexp.MustCompile(`(?i)\b(?:dual|2x|2\s*x|two)\s+(?:intel\s+)?(?:xeon|cpu|processor)`)
)

type ComputeSpec struct {
	Class          string    `docstore:"class"`
	IsCompute      bool      `docstore:"isCompute"`
	RejectReason   string    `docstore:"rejectReason,omitempty"`
	Brand          string    `docstore:"brand,omitempty"`
	Family         string    `docstore:"family,omitempty"`
	Model          string    `docstore:"model,omitempty"`
	Generation     string    `docstore:"generation,omitempty"`
	CPUModel       string    `docstore:"cpuModel,omitempty"`
	CPUCount       int       `docstore:"cpuCount,omitempty"`
	CoreCount      int       `docstore:"coreCount,omitempty"`
	RAMGB          float64   `docstore:"ramGB,omitempty"`
	RAMType        string    `docstore:"ramType,omitempty"`
	SSDTB          float64   `docstore:"ssdTB,omitempty"`
	HDDTB          float64   `docstore:"hddTB,omitempty"`
	StorageSummary string    `docstore:"storageSummary,omitempty"`
	GPU            string    `docstore:"gpu,omitempty"`
	Condition      string    `docstore:"condition,omitempty"`
	CanonicalText  string    `docstore:"canonicalText,omitempty"`
	ParsedAt       time.Time `docstore:"parsedAt,omitempty"`
}

type ComputeObservation struct {
	Product
	Spec                    ComputeSpec                 `docstore:"spec"`
	EmbeddingText           string                      `docstore:"embeddingText,omitempty"`
	EmbeddingModel          string                      `docstore:"embeddingModel,omitempty"`
	EmbeddingVector         []float64                   `docstore:"embeddingVector,omitempty"`
	ComparableCount         int                         `docstore:"computeComparableCount,omitempty"`
	ComparableMedianPrice   float64                     `docstore:"computeComparableMedianPrice,omitempty"`
	ComparableP25Price      float64                     `docstore:"computeComparableP25Price,omitempty"`
	OutlierScore            float64                     `docstore:"outlierScore,omitempty"`
	OutlierGapPct           float64                     `docstore:"outlierGapPct,omitempty"`
	OutlierGapAmount        float64                     `docstore:"outlierGapAmount,omitempty"`
	IsWarm                  bool                        `docstore:"isWarm"`
	IsLavaHot               bool                        `docstore:"isLavaHot"`
	Summary                 string                      `docstore:"summary,omitempty"`
	EbaySoldQuery           string                      `docstore:"ebaySoldQuery,omitempty"`
	EbaySoldBackend         string                      `docstore:"ebaySoldBackend,omitempty"`
	EbaySoldComparableCount int                         `docstore:"ebaySoldComparableCount,omitempty"`
	EbaySoldMedianPrice     float64                     `docstore:"ebaySoldMedianPrice,omitempty"`
	EbaySoldP25Price        float64                     `docstore:"ebaySoldP25Price,omitempty"`
	EbaySoldGapPct          float64                     `docstore:"ebaySoldGapPct,omitempty"`
	EbaySoldGapAmount       float64                     `docstore:"ebaySoldGapAmount,omitempty"`
	EbaySoldVerdict         string                      `docstore:"ebaySoldVerdict,omitempty"`
	EbaySoldCheckedAt       time.Time                   `docstore:"ebaySoldCheckedAt,omitempty"`
	EbaySoldAlertKey        string                      `docstore:"ebaySoldAlertKey,omitempty"`
	EbaySoldError           string                      `docstore:"ebaySoldError,omitempty"`
	EbaySoldComparables     []ComputeExternalComparable `docstore:"ebaySoldComparables,omitempty"`
	FirstSeen               time.Time                   `docstore:"firstSeen,omitempty"`
	LastSeen                time.Time                   `docstore:"lastSeen,omitempty"`
	LastAlertAt             time.Time                   `docstore:"lastAlertAt,omitempty"`
	LastAlertKey            string                      `docstore:"lastAlertKey,omitempty"`
	LastIssueAlertAt        time.Time                   `docstore:"lastIssueAlertAt,omitempty"`
	LastIssueAlertKey       string                      `docstore:"lastIssueAlertKey,omitempty"`
}

type ComputeExternalComparable struct {
	Title      string      `docstore:"title"`
	CleanTitle string      `docstore:"cleanTitle,omitempty"`
	Price      float64     `docstore:"price"`
	Source     string      `docstore:"source"`
	Query      string      `docstore:"query,omitempty"`
	Backend    string      `docstore:"backend,omitempty"`
	Spec       ComputeSpec `docstore:"spec,omitempty"`
	ObservedAt time.Time   `docstore:"observedAt,omitempty"`
}

type ComputeScore struct {
	ComparableCount int
	MedianPrice     float64
	P25Price        float64
	GapPct          float64
	GapAmount       float64
	Score           float64
	IsWarm          bool
	IsLavaHot       bool
	Summary         string
}

type ComputeIssue struct {
	Title        string
	Severity     string
	Reason       string
	Details      string
	Product      Product
	Spec         ComputeSpec
	Score        ComputeScore
	Verification EbaySoldVerification
	OccurredAt   time.Time
}

func ParseComputeSpec(product Product) ComputeSpec {
	title := strings.TrimSpace(product.Name)
	titleLower := strings.ToLower(title)
	haystack := computeHaystack(product)
	lower := strings.ToLower(haystack)
	spec := ComputeSpec{
		Class:     ComputeClassOther,
		Brand:     normalizeBrand(firstNonEmpty(product.BrandName, brandFromText(title), brandFromText(haystack))),
		Condition: conditionFromText(firstNonEmpty(title, haystack)),
		ParsedAt:  time.Now(),
	}

	if reason := rejectComputeReason(lower); reason != "" {
		spec.Class = ComputeClassAccessory
		spec.RejectReason = reason
		spec.CanonicalText = computeCanonicalText(product, spec)
		return spec
	}

	spec.Family, spec.Model, spec.Generation = computeFamilyModel(title)
	if spec.Family == "" && spec.Model == "" {
		spec.Family, spec.Model, spec.Generation = computeFamilyModel(haystack)
	}
	spec.CPUModel = firstNonEmpty(cpuModelFromText(title), cpuModelFromText(haystack))
	spec.CPUCount = cpuCountFromText(haystack)
	spec.CoreCount = firstPositiveInt(coreCountFromText(title), intFromSpec(product.Specs, "processorcores"), coreCountFromText(haystack))
	spec.RAMGB = firstPositiveFloat(ramGBFromText(title), floatFromSpec(product.Specs, "ramsize"), ramGBFromText(haystack))
	spec.RAMType = firstNonEmpty(ramTypeFromText(title), ramTypeFromText(haystack))
	spec.SSDTB, spec.HDDTB, spec.StorageSummary = storageFromText(title)
	if spec.SSDTB == 0 && spec.HDDTB == 0 {
		spec.SSDTB, spec.HDDTB, spec.StorageSummary = storageFromText(haystack)
	}
	spec.GPU = firstNonEmpty(gpuFromText(title), gpuFromText(haystack))
	spec.Class = computeClassFromText(haystack, spec)
	if titleLower != "" {
		if titleClass := computeClassFromText(title, spec); titleClass != ComputeClassOther {
			spec.Class = titleClass
		}
	}
	spec.IsCompute = isHighComputeSpec(spec)
	if !spec.IsCompute {
		spec.RejectReason = "not_high_compute"
	}
	if title == "" {
		spec.RejectReason = "missing_title"
		spec.IsCompute = false
	}
	spec.CanonicalText = computeCanonicalText(product, spec)
	return spec
}

func ComputeEmbeddingText(product Product, spec ComputeSpec) string {
	if spec.CanonicalText != "" {
		return spec.CanonicalText
	}
	return computeCanonicalText(product, spec)
}

func ScoreComputeOutlier(product Product, spec ComputeSpec, comps []ComputeObservation) ComputeScore {
	return ScoreComputeObservationOutlier(ComputeObservation{Product: product, Spec: spec}, comps)
}

func ScoreComputeObservationOutlier(observation ComputeObservation, comps []ComputeObservation) ComputeScore {
	product := observation.Product
	spec := observation.Spec
	price := effectiveProductPrice(product)
	if price <= 0 || !spec.IsCompute {
		return ComputeScore{}
	}
	prices := computeComparablePrices(observation, comps)
	if len(prices) < computeMinComparableCount {
		return ComputeScore{ComparableCount: len(prices)}
	}
	sort.Float64s(prices)
	medianPrice := percentileSorted(prices, 0.50)
	p25Price := percentileSorted(prices, 0.25)
	if medianPrice <= price {
		return ComputeScore{ComparableCount: len(prices), MedianPrice: medianPrice, P25Price: p25Price}
	}
	gapAmount := medianPrice - price
	gapPct := gapAmount / medianPrice * 100
	score := gapPct + math.Min(gapAmount/20, 50)
	if spec.RAMGB >= 64 {
		score += 8
	}
	if spec.RAMGB >= 128 {
		score += 10
	}
	if spec.CoreCount >= 16 {
		score += 6
	}
	if spec.CoreCount >= 24 {
		score += 8
	}

	warm := gapPct >= defaultComputeWarmMinGapPct && gapAmount >= defaultComputeWarmMinGapAmount && price <= p25Price+priceComparisonEpsilon
	hot := gapPct >= defaultComputeHotMinGapPct && gapAmount >= defaultComputeHotMinGapAmount && price <= p25Price+priceComparisonEpsilon
	if hot {
		warm = true
	}

	return ComputeScore{
		ComparableCount: len(prices),
		MedianPrice:     medianPrice,
		P25Price:        p25Price,
		GapPct:          gapPct,
		GapAmount:       gapAmount,
		Score:           score,
		IsWarm:          warm,
		IsLavaHot:       hot,
		Summary:         computeSummary(spec, len(prices), medianPrice, gapAmount, gapPct),
	}
}

type computeComparablePrice struct {
	price      float64
	similarity float64
	hasVector  bool
}

func computeComparablePrices(observation ComputeObservation, comps []ComputeObservation) []float64 {
	values := make([]computeComparablePrice, 0, len(comps))
	vectorComparableCount := 0
	seen := make(map[string]bool, len(comps))
	for _, comp := range comps {
		if !compatibleComputeComp(observation.Product, observation.Spec, comp) {
			continue
		}
		key := computeObservationKey(comp.Product)
		if key != "" {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		compPrice := effectiveProductPrice(comp.Product)
		if compPrice <= 0 {
			continue
		}
		value := computeComparablePrice{price: compPrice}
		if similarity, ok := vectorSimilarity(observation.EmbeddingVector, comp.EmbeddingVector); ok {
			value.similarity = similarity
			value.hasVector = true
			vectorComparableCount++
		}
		values = append(values, value)
	}
	values = append(values, externalComparablePrices(observation, comps)...)
	if len(values) == 0 {
		return nil
	}

	if vectorComparableCount >= computeEmbeddingMinComparableCount {
		nearest := make([]computeComparablePrice, 0, len(values))
		for _, value := range values {
			if value.hasVector && value.similarity >= computeEmbeddingSimilarityCutoff {
				nearest = append(nearest, value)
			}
		}
		if len(nearest) >= computeEmbeddingMinComparableCount {
			sort.Slice(nearest, func(i, j int) bool {
				if nearest[i].similarity == nearest[j].similarity {
					return nearest[i].price < nearest[j].price
				}
				return nearest[i].similarity > nearest[j].similarity
			})
			if len(nearest) > computeEmbeddingMaxComparableCount {
				nearest = nearest[:computeEmbeddingMaxComparableCount]
			}
			return comparablePriceValues(nearest)
		}
	}
	return comparablePriceValues(values)
}

func externalComparablePrices(observation ComputeObservation, comps []ComputeObservation) []computeComparablePrice {
	values := make([]computeComparablePrice, 0)
	seen := make(map[string]bool)
	for _, comp := range comps {
		for _, external := range comp.EbaySoldComparables {
			if external.Price <= 0 || strings.TrimSpace(external.Title) == "" {
				continue
			}
			externalSpec := external.Spec
			if externalSpec.ParsedAt.IsZero() || externalSpec.Class == "" {
				externalSpec = ParseComputeSpec(Product{Name: external.Title, SalePrice: external.Price, Source: external.Source})
			}
			externalObservation := ComputeObservation{
				Product: Product{
					SKU:        "external:" + external.Source + ":" + external.Title,
					Name:       external.Title,
					SalePrice:  external.Price,
					Source:     external.Source,
					SellerID:   "external:" + external.Source,
					SellerName: external.Source,
				},
				Spec: externalSpec,
			}
			if !compatibleExternalComputeComp(observation, externalObservation) {
				continue
			}
			key := fmt.Sprintf("%s|%.2f", strings.ToLower(external.Title), external.Price)
			if seen[key] {
				continue
			}
			seen[key] = true
			values = append(values, computeComparablePrice{price: external.Price})
		}
	}
	return values
}

func comparablePriceValues(values []computeComparablePrice) []float64 {
	prices := make([]float64, 0, len(values))
	for _, value := range values {
		prices = append(prices, value.price)
	}
	return prices
}

func compatibleExternalComputeComp(observation, comp ComputeObservation) bool {
	if !comp.Spec.IsCompute {
		return false
	}
	spec := observation.Spec
	compSpec := comp.Spec
	if spec.Class != "" && compSpec.Class != "" && spec.Class != compSpec.Class {
		if !sameComputeClassGroup(spec.Class, compSpec.Class) {
			return false
		}
	}
	if spec.Family != "" && compSpec.Family != "" && spec.Family != compSpec.Family {
		return false
	}
	if spec.Model != "" && compSpec.Model != "" && spec.Model != compSpec.Model {
		return false
	}
	if spec.RAMGB > 0 && compSpec.RAMGB > 0 {
		ratio := compSpec.RAMGB / spec.RAMGB
		if ratio < minimumComparableRAMRatio(spec) || ratio > 2.0 {
			return false
		}
	}
	if spec.CoreCount > 0 && compSpec.CoreCount > 0 {
		diff := math.Abs(float64(spec.CoreCount - compSpec.CoreCount))
		if diff > math.Max(12, float64(spec.CoreCount)) {
			return false
		}
	}
	if spec.GPU != "" || compSpec.GPU != "" {
		if !similarGPU(spec.GPU, compSpec.GPU) {
			return false
		}
	}
	return true
}

func vectorSimilarity(a, b []float64) (float64, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot, true
}

func compatibleComputeComp(product Product, spec ComputeSpec, comp ComputeObservation) bool {
	if comp.SKU == product.SKU && comp.Source == product.Source {
		return false
	}
	if sameBestBuySeller(product, comp.SellerID, comp.SellerName) {
		return false
	}
	compSpec := comp.Spec
	if !compSpec.IsCompute {
		return false
	}
	if spec.Class != "" && compSpec.Class != "" && spec.Class != compSpec.Class {
		if !sameComputeClassGroup(spec.Class, compSpec.Class) {
			return false
		}
	}
	if spec.Family != "" && compSpec.Family != "" && spec.Family != compSpec.Family {
		return false
	}
	if spec.Model != "" && compSpec.Model != "" && spec.Model != compSpec.Model {
		return false
	}
	if spec.Family == "" && compSpec.Family == "" {
		if !similarCPUClass(spec.CPUModel, compSpec.CPUModel) && !similarResourceBand(spec, compSpec) {
			return false
		}
	}
	if spec.Family != "" && compSpec.Family == "" && !similarResourceBand(spec, compSpec) {
		return false
	}
	if spec.Family == "" && compSpec.Family != "" && !similarResourceBand(spec, compSpec) {
		return false
	}
	if spec.RAMGB > 0 && compSpec.RAMGB > 0 {
		ratio := compSpec.RAMGB / spec.RAMGB
		if ratio < 0.5 || ratio > 2.0 {
			return false
		}
	}
	if spec.GPU != "" || compSpec.GPU != "" {
		if !similarGPU(spec.GPU, compSpec.GPU) {
			return false
		}
	}
	if spec.CoreCount > 0 && compSpec.CoreCount > 0 {
		diff := math.Abs(float64(spec.CoreCount - compSpec.CoreCount))
		if diff > math.Max(8, float64(spec.CoreCount)/2) {
			return false
		}
	}
	return true
}

func similarGPU(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return normalizeGPU(a) == normalizeGPU(b)
}

func normalizeGPU(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	prefixes := []string{
		"nvidia quadro ",
		"nvidia geforce ",
		"nvidia tesla ",
		"nvidia ",
		"geforce ",
		"quadro ",
		"rtx ",
		"radeon pro ",
		"tesla ",
		"amd instinct ",
		"amd ",
		"intel data center gpu ",
		"intel ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			s = strings.TrimPrefix(s, p)
			break
		}
	}
	return strings.Join(strings.Fields(s), "")
}

func isExtremeComputeSpec(spec ComputeSpec, price float64) bool {
	if price <= 0 {
		return false
	}
	if price <= 100 {
		return spec.RAMGB >= 128 || spec.CoreCount >= 16 || highValueCPU(spec.CPUModel)
	}
	if price <= 1000 {
		return spec.RAMGB >= 256 || spec.CoreCount >= 24 || strings.Contains(strings.ToLower(spec.CPUModel), "epyc") || strings.Contains(strings.ToLower(spec.CPUModel), "threadripper")
	}
	return spec.RAMGB >= 512 || spec.CoreCount >= 32
}

func minimumComparableRAMRatio(spec ComputeSpec) float64 {
	switch {
	case spec.RAMGB >= 512:
		return 0.125
	case spec.RAMGB >= 256:
		return 0.25
	default:
		return 0.75
	}
}

func similarResourceBand(a, b ComputeSpec) bool {
	if a.RAMGB <= 0 || b.RAMGB <= 0 {
		return false
	}
	ramRatio := b.RAMGB / a.RAMGB
	if ramRatio < 0.75 || ramRatio > 1.5 {
		return false
	}
	if a.CoreCount > 0 && b.CoreCount > 0 {
		diff := math.Abs(float64(a.CoreCount - b.CoreCount))
		return diff <= math.Max(4, float64(a.CoreCount)/3)
	}
	return similarCPUClass(a.CPUModel, b.CPUModel)
}

func similarCPUClass(a, b string) bool {
	a = cpuClass(a)
	b = cpuClass(b)
	return a != "" && a == b
}

func cpuClass(cpu string) string {
	cpu = strings.ToLower(cpu)
	switch {
	case strings.Contains(cpu, "xeon"):
		return "xeon"
	case strings.Contains(cpu, "threadripper"):
		return "threadripper"
	case strings.Contains(cpu, "epyc"):
		return "epyc"
	case strings.Contains(cpu, "core i9") || strings.Contains(cpu, "ultra 9"):
		return "core_i9"
	case strings.Contains(cpu, "core i7") || strings.Contains(cpu, "ultra 7"):
		return "core_i7"
	case strings.Contains(cpu, "ryzen 9"):
		return "ryzen_9"
	case strings.Contains(cpu, "ryzen 7"):
		return "ryzen_7"
	default:
		return ""
	}
}

func computeHaystack(product Product) string {
	parts := []string{product.Name, product.CategoryName, product.BrandName, product.ModelNumber}
	if len(product.Specs) > 0 {
		keys := make([]string, 0, len(product.Specs))
		for key := range product.Specs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(parts, key, product.Specs[key])
		}
	}
	return strings.Join(parts, " ")
}

func rejectComputeReason(lower string) string {
	rejects := map[string]string{
		"rail":                  "server_rail_or_mount",
		"rack cabinet":          "rack_accessory",
		"rack mount shelf":      "rack_accessory",
		"rack shelf":            "rack_accessory",
		"mounting shelf":        "rack_accessory",
		"mount shelf":           "rack_accessory",
		"shelf":                 "rack_accessory",
		"open frame":            "rack_accessory",
		"pdu":                   "power_accessory",
		"power strip":           "power_accessory",
		"cooling fan":           "server_part",
		"cpu fan":               "server_part",
		"case fan":              "server_part",
		"system fan":            "server_part",
		"fan kit":               "server_part",
		"fan module":            "server_part",
		"fan assembly":          "server_part",
		"blower fan":            "server_part",
		"front access storage":  "storage_component",
		"storage enclosure":     "storage_component",
		"ethernet adapter":      "network_adapter",
		"gigabit adapter":       "network_adapter",
		"wlan module":           "network_adapter",
		"wi-fi module":          "network_adapter",
		"wifi module":           "network_adapter",
		"dc power jack":         "power_accessory",
		"heatsink":              "server_part",
		"heat sink":             "server_part",
		"riser kit":             "server_part",
		"riser card":            "server_part",
		"riser board":           "server_part",
		"power supply":          "server_part",
		"drive bay":             "server_part",
		"drive tray":            "server_part",
		"tray caddy":            "server_part",
		"hdd tray":              "server_part",
		"motherboard":           "server_part",
		"main board":            "server_part",
		"mainboard":             "server_part",
		"logic board":           "server_part",
		"system board":          "server_part",
		"esc board":             "server_part",
		"power board":           "server_part",
		"backplane":             "server_part",
		"bezel":                 "server_part",
		"ac adapter":            "power_accessory",
		"ac charger":            "power_accessory",
		"thin client":           "low_power_desktop",
		"zero client":           "low_power_desktop",
		"power cord":            "power_accessory",
		"power adapter":         "power_accessory",
		"charger fit":           "power_accessory",
		"battery pack":          "accessory",
		"replacement battery":   "accessory",
		"ink cartridge":         "not_compute",
		"toner cartridge":       "not_compute",
		"laptop bag":            "accessory",
		"laptop case":           "accessory",
		"laptop sleeve":         "accessory",
		"cable":                 "accessory",
		"battery":               "accessory",
		"lcd screen":            "accessory",
		"screen replacement":    "accessory",
		"keyboard":              "accessory",
		"gaming mouse":          "accessory",
		"optical sensor":        "accessory",
		"duster filter":         "accessory",
		"compatible with":       "component",
		"enablement kit":        "server_part",
		"memory ram compatible": "component",
		"computer processors":   "processor_component",
		"bench buffer":          "not_compute",
		"chafing dish":          "not_compute",
		"buffet":                "not_compute",
		"screwdriver":           "accessory",
		"tool kit":              "accessory",
		"toolkit":               "accessory",
		"pry tool":              "accessory",
		"spudger":               "accessory",
	}
	for needle, reason := range rejects {
		if strings.Contains(lower, needle) {
			return reason
		}
	}
	if strings.Contains(lower, "processor upgrade") && !strings.Contains(lower, "desktop") {
		return "component"
	}
	if regexp.MustCompile(`(?i)\b(?:gpu\s+)?graphics?\s+card\b`).MatchString(lower) && !containsWholeComputeSystemTerm(lower) {
		return "gpu_component"
	}
	if strings.Contains(lower, " processor") && !containsComputeSystemTerm(lower) {
		return "processor_component"
	}
	if regexp.MustCompile(`(?i)\b\d+\s*(?:gb|tb)\s+(?:ssd|hdd|hard drive|sas|sata)\b`).MatchString(lower) &&
		!containsComputeSystemTerm(lower) {
		return "storage_component"
	}
	return ""
}

func containsComputeSystemTerm(lower string) bool {
	terms := []string{
		"desktop",
		"workstation",
		"server",
		"laptop",
		"notebook",
		"chromebook",
		"mini pc",
		"gaming pc",
		"tower",
		"mac studio",
		"macbook",
		"mac pro",
		"poweredge",
		"proliant",
		"thinksystem",
		"thinkstation",
		"zbook",
	}
	for _, term := range terms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func containsWholeComputeSystemTerm(lower string) bool {
	terms := []string{
		"desktop",
		"server",
		"laptop",
		"notebook",
		"mini pc",
		"gaming pc",
		"workstation pc",
		"workstation computer",
		"tower pc",
		"desktop pc",
		"poweredge",
		"proliant",
		"thinksystem",
		"zbook",
		"mac studio",
		"macbook",
		"mac pro",
	}
	for _, term := range terms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func computeClassFromText(text string, spec ComputeSpec) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "rackstation") || strings.Contains(lower, "diskstation") || strings.Contains(lower, "beestation") || strings.Contains(lower, " qnap ") || strings.Contains(lower, " nas "):
		return ComputeClassNAS
	case strings.Contains(lower, "rackmount") || strings.Contains(lower, "rack mount") || strings.Contains(lower, "poweredge r") || strings.Contains(lower, "proliant dl") || strings.Contains(lower, "thinksystem sr"):
		return ComputeClassRackServer
	case strings.Contains(lower, "tower server") || strings.Contains(lower, "proliant ml") || strings.Contains(lower, "microserver") || strings.Contains(lower, "poweredge t"):
		return ComputeClassTowerServer
	case strings.Contains(lower, "workstation laptop") || strings.Contains(lower, "zbook") || strings.Contains(lower, "precision 7") || strings.Contains(lower, "thinkpad p"):
		return ComputeClassWorkstationLaptop
	case strings.Contains(lower, "mac studio"):
		return ComputeClassWorkstationDesktop
	case strings.Contains(lower, "workstation") || strings.Contains(lower, "precision") || strings.Contains(lower, "thinkstation") || strings.Contains(lower, " hp z") || strings.Contains(lower, "mac pro"):
		return ComputeClassWorkstationDesktop
	case strings.Contains(lower, "chromebook"):
		return ComputeClassOther
	case strings.Contains(lower, "laptop") || strings.Contains(lower, "notebook") || strings.Contains(lower, "macbook"):
		return ComputeClassLaptop
	case strings.Contains(lower, "desktop") || strings.Contains(lower, "gaming pc") || strings.Contains(lower, "mini pc"):
		return ComputeClassDesktop
	case spec.CPUModel != "" || spec.RAMGB >= 64 || spec.CoreCount >= 12:
		return ComputeClassDesktop
	default:
		return ComputeClassOther
	}
}

func isHighComputeSpec(spec ComputeSpec) bool {
	if spec.Class == ComputeClassAccessory || spec.Class == ComputeClassComponent || spec.Class == ComputeClassOther {
		return false
	}
	if spec.Class == ComputeClassRackServer || spec.Class == ComputeClassTowerServer {
		return true
	}
	if spec.Class == ComputeClassWorkstationDesktop {
		return spec.RAMGB >= 32 || spec.CoreCount >= 12 || highValueCPU(spec.CPUModel) || spec.GPU != ""
	}
	if spec.Class == ComputeClassWorkstationLaptop || spec.Class == ComputeClassLaptop {
		return spec.RAMGB >= 64
	}
	return spec.RAMGB >= 64 || spec.CoreCount >= 16 || highValueCPU(spec.CPUModel) || spec.GPU != ""
}

func computeFamilyModel(text string) (string, string, string) {
	lower := strings.ToLower(text)
	patterns := []struct {
		family string
		re     *regexp.Regexp
		gen    func(string) string
	}{
		{"dell_poweredge", regexp.MustCompile(`(?i)\bpoweredge\s+([rt]\d{3,4}(?:xd)?)\b`), dellGeneration},
		{"hpe_proliant", regexp.MustCompile(`(?i)\bproliant\s+((?:dl|ml)\d{2,3}p?)\s*(gen\s*\d+|g\d+)?\b`), hpeGeneration},
		{"dell_precision", regexp.MustCompile(`(?i)\bprecision\s+(\d{4})\b`), func(string) string { return "" }},
		{"hp_z", regexp.MustCompile(`(?i)\bhp\s+z([2486]\s*g\d+|[0-9]\s*g\d+|[0-9]+)\b`), func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }},
		{"lenovo_thinkstation", regexp.MustCompile(`(?i)\bthinkstation\s+([pst]\d+[a-z0-9]*)\b`), func(string) string { return "" }},
		{"apple_mac_studio", regexp.MustCompile(`(?i)\bmac\s+studio\s*(m[1-9]\s*(?:max|ultra|pro)?)?\b`), func(string) string { return "" }},
		{"apple_macbook_pro", regexp.MustCompile(`(?i)\bmacbook\s+pro\s*(m[1-9]\s*(?:max|ultra|pro)?)?\b`), func(string) string { return "" }},
		{"apple_macbook_air", regexp.MustCompile(`(?i)\bmacbook\s+air\s*(m[1-9]\s*(?:max|ultra|pro)?)?\b`), func(string) string { return "" }},
		{"apple_mac_pro", regexp.MustCompile(`(?i)\bmac\s+pro\s*(a\d{4}|late\s+\d{4})?\b`), func(string) string { return "" }},
	}
	for _, pattern := range patterns {
		if match := pattern.re.FindStringSubmatch(text); len(match) > 1 {
			model := normalizeModel(match[1])
			gen := ""
			if len(match) > 2 {
				gen = pattern.gen(match[2])
			}
			if gen == "" {
				gen = pattern.gen(model)
			}
			return pattern.family, model, gen
		}
	}
	switch {
	case strings.Contains(lower, "xeon"):
		return "xeon_compute", "", ""
	case strings.Contains(lower, "threadripper"):
		return "threadripper_compute", "", ""
	case strings.Contains(lower, "epyc"):
		return "epyc_compute", "", ""
	default:
		return "", "", ""
	}
}

func normalizeModel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func dellGeneration(model string) string {
	model = strings.ToLower(model)
	if len(model) < 4 {
		return ""
	}
	switch model[1] {
	case '2':
		return "12g"
	case '3':
		return "13g"
	case '4':
		return "14g"
	case '5':
		return "15g"
	case '6':
		return "16g"
	default:
		return ""
	}
}

func hpeGeneration(value string) string {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	value = strings.TrimPrefix(value, "gen")
	value = strings.TrimPrefix(value, "g")
	if value == "" {
		return ""
	}
	return "gen" + value
}

func cpuModelFromText(text string) string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bxeon\s+(?:gold|silver|bronze|w|e|e5|e7)?[-\s]?\d{3,5}[a-z]?\b`),
		regexp.MustCompile(`(?i)\bthreadripper(?:\s+pro)?\s+\d{4,5}wx\b`),
		regexp.MustCompile(`(?i)\bepyc\s+\d{4}[a-z]?\b`),
		regexp.MustCompile(`(?i)\b(?:intel\s+)?core\s+ultra\s+[579](?:\s+\d{3}[a-z0-9]*)?\b`),
		regexp.MustCompile(`(?i)\bcore\s+(?:ultra\s+)?i[579][-\s]?\d{4,5}[a-z]*\b`),
		regexp.MustCompile(`(?i)\bryzen\s+ai\s+max\b`),
		regexp.MustCompile(`(?i)\bryzen\s+ai\s+(?:max\s+)?[3579](?:\s+\d{3,4}[a-z0-9]*)?\b`),
		regexp.MustCompile(`(?i)\bryzen\s+[579]\s+\d{4,5}[a-z0-9]*\b`),
		regexp.MustCompile(`(?i)\bapple\s+m[1-9]\s*(?:pro|max|ultra)?\b`),
		regexp.MustCompile(`(?i)\bm[1-9]\s*(?:pro|max|ultra)\b`),
		regexp.MustCompile(`(?i)\bsnapdragon\s+x\s*(?:elite|plus)?(?:\s*x?\d+e?-\d+)?\b`),
	}
	for _, pattern := range patterns {
		if value := pattern.FindString(text); value != "" {
			return strings.Join(strings.Fields(value), " ")
		}
	}
	if strings.Contains(strings.ToLower(text), "xeon") {
		return "xeon"
	}
	return ""
}

func cpuCountFromText(text string) int {
	if cpuCountPattern.MatchString(text) {
		return 2
	}
	return 1
}

func coreCountFromText(text string) int {
	for _, pattern := range corePatterns {
		if match := pattern.FindStringSubmatch(text); len(match) > 1 {
			if value, err := strconv.Atoi(match[1]); err == nil && value > 0 && value < 256 {
				return value
			}
		}
	}
	return 0
}

func ramGBFromText(text string) float64 {
	bestDirect := 0.0
	bestPrefix := 0.0
	bestMemory := 0.0
	bestUnlabeled := 0.0

	for i, pattern := range ramPatterns {
		matches := pattern.FindAllStringSubmatch(text, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			value, err := strconv.ParseFloat(match[1], 64)
			if err != nil || value <= 0 || value > 2048 {
				continue
			}
			switch i {
			case 0, 4:
				if value > bestDirect {
					bestDirect = value
				}
			case 1:
				if value > bestMemory {
					bestMemory = value
				}
			case 2:
				if value > bestPrefix {
					bestPrefix = value
				}
			case 3:
				// Pattern 3 is the unlabeled pipe pattern "| 16GB |"
				if value > bestUnlabeled {
					bestUnlabeled = value
				}
			}
		}
	}

	if bestDirect > 0 {
		return bestDirect
	}
	if bestPrefix > 0 {
		return bestPrefix
	}
	if bestMemory > 0 {
		return bestMemory
	}
	return bestUnlabeled
}

func ramTypeFromText(text string) string {
	lower := strings.ToLower(text)
	for _, token := range []string{"ddr5", "ddr4", "ddr3", "lpddr5x", "lpddr5", "ecc", "rdimm"} {
		if strings.Contains(lower, token) {
			return token
		}
	}
	return ""
}

func storageFromText(text string) (float64, float64, string) {
	var ssdTB, hddTB float64
	for _, pattern := range storagePatterns {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			if len(match) < 5 {
				continue
			}
			count := 1.0
			if match[1] != "" {
				if parsed, err := strconv.ParseFloat(match[1], 64); err == nil && parsed > 0 {
					count = parsed
				}
			}
			size, err := strconv.ParseFloat(match[2], 64)
			if err != nil || size <= 0 {
				continue
			}
			unit := strings.ToLower(match[3])
			kind := strings.ToLower(match[4])
			tb := size * count
			if unit == "gb" {
				tb = tb / 1000
			}
			if strings.Contains(kind, "ssd") || strings.Contains(kind, "nvme") || strings.Contains(kind, "solid state") {
				ssdTB += tb
			} else {
				hddTB += tb
			}
		}
	}
	var parts []string
	if ssdTB > 0 {
		parts = append(parts, fmt.Sprintf("%.1fTB SSD", ssdTB))
	}
	if hddTB > 0 {
		parts = append(parts, fmt.Sprintf("%.1fTB HDD", hddTB))
	}
	return ssdTB, hddTB, strings.Join(parts, ", ")
}

func gpuFromText(text string) string {
	patterns := []*regexp.Regexp{
		// Modern NVIDIA Server/AI GPUs (e.g., A40, L40S, A100, H100, V100, B200, A2, L4)
		// We require 'nvidia' or 'rtx' prefix if it's a very short name like A2 to prevent false positives.
		regexp.MustCompile(`(?i)\b(?:nvidia\s+|rtx\s+)[alvhb]\d{1,3}[a-z]{0,4}\b`),
		regexp.MustCompile(`(?i)\b[alvhb]\d{2,3}[a-z]{0,4}\b`), // Naked A100, H100, etc. (2-3 digits safe)
		// NVIDIA Grace/Blackwell Superchips (e.g., GH200, GB200)
		regexp.MustCompile(`(?i)\b(?:nvidia\s+)?(?:gh|gb)\d{3}\b`),
		// NVIDIA Tesla series with explicit prefix (e.g., Tesla V100, NVIDIA T4)
		regexp.MustCompile(`(?i)\b(?:nvidia\s+|tesla\s+)[kmptvc]\d{1,2}0?[a-z]?\b`),
		// Naked Tesla series (e.g., T4, P40). Exclude 'v' to avoid Xeon v1/v2/v3/v4 matches
		regexp.MustCompile(`(?i)\b(?:t4|p4|p40|k80|m10|m40|m60)\b`),
		// NVIDIA GRID series (e.g., GRID K1, GRID M10)
		regexp.MustCompile(`(?i)\b(?:nvidia\s+)?grid\s+[km]\d{1,2}\b`),
		// AMD Instinct (e.g., MI300X, MI250, MI50)
		regexp.MustCompile(`(?i)\b(?:amd\s+)?instinct\s+mi\d{2,3}[a-z]?\b`),
		// AMD FirePro S-Series (Server) (e.g., FirePro S9150, S7150)
		regexp.MustCompile(`(?i)\b(?:amd\s+)?firepro\s+s\d{4}\b`),
		// Intel Data Center GPUs (e.g., Flex 140, Max 1550)
		regexp.MustCompile(`(?i)\b(?:intel\s+)?(?:data\s+center\s+gpu\s+)?(?:max|flex)\s+\d{3,4}\b`),
		// Intel Gaudi AI Accelerators (e.g., Gaudi 2, Gaudi3)
		regexp.MustCompile(`(?i)\b(?:intel\s+)?gaudi\s*\d?\b`),
		// NVIDIA RTX A-series workstation (e.g., RTX A4000)
		regexp.MustCompile(`(?i)\brtx\s+a\d{4}\b`),
		// NVIDIA GeForce GTX/RTX cards commonly bundled in lower-end workstations.
		regexp.MustCompile(`(?i)\b(?:nvidia\s+)?(?:geforce\s+)?(?:gtx|rtx)\s+\d{3,4}(?:\s*ti)?\b`),
		// NVIDIA Quadro specific
		regexp.MustCompile(`(?i)\bquadro\s+[a-z]?\d{3,4}\b`),
		// NVIDIA workstation generic (P1000, K2000, T1000, etc.)
		regexp.MustCompile(`(?i)\b(?:nvidia\s+)?(?:quadro\s+)?[kmpt]\d{3,4}m?\b`),
		// AMD Radeon Pro
		regexp.MustCompile(`(?i)\bradeon\s+pro\s+[a-z0-9\s]+`),
	}
	for _, pattern := range patterns {
		if value := pattern.FindString(text); value != "" {
			return strings.Join(strings.Fields(value), " ")
		}
	}
	return ""
}

func conditionFromText(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "open box"):
		return "open_box"
	case strings.Contains(lower, "refurbished excellent"):
		return "refurbished_excellent"
	case strings.Contains(lower, "refurbished good"):
		return "refurbished_good"
	case strings.Contains(lower, "refurbished fair"):
		return "refurbished_fair"
	case strings.Contains(lower, "refurbished"):
		return "refurbished"
	default:
		return "new_or_unspecified"
	}
}

func brandFromText(text string) string {
	lower := strings.ToLower(text)
	for _, brand := range []string{"dell", "hpe", "hp", "lenovo", "apple", "supermicro", "synology", "qnap", "asus"} {
		if strings.Contains(lower, brand) {
			return brand
		}
	}
	return ""
}

func normalizeBrand(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "hewlett packard", "hpe", "hp inc.", "hp":
		return "hp"
	default:
		return value
	}
}

func computeCanonicalText(product Product, spec ComputeSpec) string {
	fields := []string{
		"class:" + firstNonEmpty(spec.Class, ComputeClassOther),
		"brand:" + spec.Brand,
		"family:" + spec.Family,
		"model:" + spec.Model,
		"generation:" + spec.Generation,
		"cpu:" + spec.CPUModel,
		fmt.Sprintf("cpu_count:%d", spec.CPUCount),
		fmt.Sprintf("cores:%d", spec.CoreCount),
		fmt.Sprintf("ram_gb:%.0f", spec.RAMGB),
		"ram_type:" + spec.RAMType,
		fmt.Sprintf("ssd_tb:%.1f", spec.SSDTB),
		fmt.Sprintf("hdd_tb:%.1f", spec.HDDTB),
		"gpu:" + spec.GPU,
		"condition:" + spec.Condition,
		"category:" + product.CategoryName,
		"title:" + product.Name,
	}
	return strings.Join(compactStrings(fields), "; ")
}

func computeSummary(spec ComputeSpec, count int, medianPrice, gapAmount, gapPct float64) string {
	details := []string{}
	if spec.Model != "" {
		details = append(details, spec.Model)
	} else if spec.Family != "" {
		details = append(details, strings.ReplaceAll(spec.Family, "_", " "))
	}
	if spec.RAMGB > 0 {
		details = append(details, fmt.Sprintf("%.0fGB RAM", spec.RAMGB))
	}
	if spec.CoreCount > 0 {
		details = append(details, fmt.Sprintf("%d cores", spec.CoreCount))
	}
	if spec.GPU != "" {
		details = append(details, spec.GPU)
	}
	return fmt.Sprintf("%s looks %.0f%% ($%.0f) below %d other-seller compute comps; median $%.0f.",
		firstNonEmpty(strings.Join(details, ", "), "Compute config"), gapPct, gapAmount, count, medianPrice)
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.HasSuffix(value, ":") || strings.HasSuffix(value, ":0") || strings.HasSuffix(value, ":0.0") {
			continue
		}
		out = append(out, value)
	}
	return out
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func intFromSpec(specs map[string]string, keyPart string) int {
	value := floatFromSpec(specs, keyPart)
	return int(value)
}

func floatFromSpec(specs map[string]string, keyPart string) float64 {
	keyPart = strings.ToLower(keyPart)
	for key, value := range specs {
		if !strings.Contains(strings.ToLower(key), keyPart) {
			continue
		}
		if parsed := firstNumber(value); parsed > 0 {
			return parsed
		}
	}
	return 0
}

func firstNumber(value string) float64 {
	match := regexp.MustCompile(`\d+(?:\.\d+)?`).FindString(value)
	if match == "" {
		return 0
	}
	parsed, _ := strconv.ParseFloat(match, 64)
	return parsed
}

func highValueCPU(cpu string) bool {
	cpu = strings.ToLower(cpu)
	return strings.Contains(cpu, "xeon") ||
		strings.Contains(cpu, "threadripper") ||
		strings.Contains(cpu, "epyc") ||
		strings.Contains(cpu, "core ultra 9") ||
		strings.Contains(cpu, "core i9") ||
		strings.Contains(cpu, "ryzen 9") ||
		strings.Contains(cpu, "ryzen ai max") ||
		strings.Contains(cpu, "ryzen ai 9") ||
		regexp.MustCompile(`\bm[1-9]\s*(pro|max|ultra)\b`).MatchString(cpu) ||
		strings.Contains(cpu, "snapdragon x elite")
}

func sameComputeClassGroup(a, b string) bool {
	server := map[string]bool{ComputeClassRackServer: true, ComputeClassTowerServer: true}
	workstation := map[string]bool{ComputeClassWorkstationDesktop: true, ComputeClassWorkstationLaptop: true, ComputeClassDesktop: true, ComputeClassLaptop: true}
	return (server[a] && server[b]) || (workstation[a] && workstation[b])
}

func percentileSorted(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if len(values) == 1 {
		return values[0]
	}
	pos := p * float64(len(values)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower == upper {
		return values[lower]
	}
	weight := pos - float64(lower)
	return values[lower]*(1-weight) + values[upper]*weight
}
