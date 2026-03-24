package facebook

import (
	"testing"
)

func TestVmrMakeSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Honda", "honda"},
		{"Mercedes-Benz", "mercedes-benz"},
		{"Land Rover", "land-rover"},
		{"BMW", "bmw"},
	}
	for _, tt := range tests {
		got := vmrMakeSlug(tt.input)
		if got != tt.want {
			t.Errorf("vmrMakeSlug(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestVmrModelSlug(t *testing.T) {
	tests := []struct {
		make  string
		model string
		want  string
	}{
		{"Honda", "Civic", "civic"},
		{"Honda", "CR-V", "cr-v"},
		{"Ford", "F-150", "f-150"},
		{"BMW", "3 Series", "3%20series"},
		{"Toyota", "RAV4", "rav4"},
		{"Mercedes-Benz", "C-Class", "c-class"},
		{"Suzuki", "Grand Vitara", "grand%20vitara"},
		{"Ford", "Crown Victoria", "crown%20victoria"},
		{"Dodge", "1500 Ram", "1500%20ram"},
		{"Dodge", "Grand Caravan", "grand%20caravan"},
	}
	for _, tt := range tests {
		got := vmrModelSlug(tt.make, tt.model)
		if got != tt.want {
			t.Errorf("vmrModelSlug(%q, %q) = %q, want %q", tt.make, tt.model, got, tt.want)
		}
	}
}

func TestVmrNormalize(t *testing.T) {
	tests := []struct {
		make      string
		model     string
		wantMake  string
		wantModel string
	}{
		// Ram as standalone make → Dodge
		{"Ram", "1500", "Dodge", "1500 Ram"},
		{"Ram", "2500", "Dodge", "2500 Ram"},
		{"Ram", "ProMaster", "Dodge", "ProMaster Ram"},
		// Dodge + Ram model → rearranged
		{"Dodge", "Ram 1500", "Dodge", "1500 Ram"},
		{"Dodge", "Ram 3500 TD", "Dodge", "3500 TD Ram"},
		// Non-Ram vehicles unchanged
		{"Dodge", "Challenger", "Dodge", "Challenger"},
		{"Honda", "Civic", "Honda", "Civic"},
		{"Ford", "Crown Victoria", "Ford", "Crown Victoria"},
	}
	for _, tt := range tests {
		gotMake, gotModel := vmrNormalize(tt.make, tt.model)
		if gotMake != tt.wantMake || gotModel != tt.wantModel {
			t.Errorf("vmrNormalize(%q, %q) = (%q, %q), want (%q, %q)",
				tt.make, tt.model, gotMake, gotModel, tt.wantMake, tt.wantModel)
		}
	}
}

func TestProvinceFromPostal(t *testing.T) {
	tests := []struct {
		postal string
		want   string
	}{
		{"S7K1A1", "SK"},
		{"T2P 1J9", "AB"},
		{"V5K 0A1", "BC"},
		{"M5V 3L9", "ON"},
		{"H3Z 2Y7", "QC"},
		{"R3C 4A5", "MB"},
		{"", "ON"}, // default
	}
	for _, tt := range tests {
		got := ProvinceFromPostal(tt.postal)
		if got != tt.want {
			t.Errorf("ProvinceFromPostal(%q) = %q, want %q", tt.postal, got, tt.want)
		}
	}
}

func TestReliabilityTier(t *testing.T) {
	tests := []struct {
		score float64
		want  string
	}{
		{3.67, "Tier 1"}, // Lexus
		{3.30, "Tier 1"}, // Subaru
		{3.06, "Tier 2"}, // Nissan
		{2.93, "Tier 2"}, // Ford
		{2.75, "Tier 3"}, // Hyundai
		{2.67, "Tier 3"}, // VW
		{0, "Tier 3"},    // unknown
	}
	for _, tt := range tests {
		got := ReliabilityTier(tt.score)
		if got != tt.want {
			t.Errorf("ReliabilityTier(%.2f) = %q, want %q", tt.score, got, tt.want)
		}
	}
}

func TestMileageAdjustment(t *testing.T) {
	// Vehicle: $10,000 wholesale, 3 years old
	// Normal km = 20000 * 3 = 60000
	// Factor = 0.12 * (10000/28000 + 0.27) = 0.12 * 0.6271 = 0.07526

	// Case 1: Below average mileage (40000 km) — should add value
	wsAdj, _ := mileageAdjustment(10000, 40000, 3)
	if wsAdj <= 0 {
		t.Errorf("below-average mileage should add value, got adjustment: %.2f", wsAdj)
	}

	// Case 2: Above average mileage (100000 km) — should subtract value
	wsAdj, _ = mileageAdjustment(10000, 100000, 3)
	if wsAdj >= 0 {
		t.Errorf("above-average mileage should subtract value, got adjustment: %.2f", wsAdj)
	}

	// Case 3: Exactly average mileage — adjustment should be ~0
	wsAdj, _ = mileageAdjustment(10000, 60000, 3)
	if wsAdj > 1 || wsAdj < -1 {
		t.Errorf("average mileage adjustment should be ~0, got: %.2f", wsAdj)
	}

	// Case 4: Cap at 70% of wholesale
	wsAdj, _ = mileageAdjustment(10000, 500000, 1)
	if wsAdj < -7000 {
		t.Errorf("adjustment should be capped at -7000 (70%% of 10000), got: %.2f", wsAdj)
	}
}

func TestParseVMRTrims(t *testing.T) {
	html := `<html><body>
<form name="pricing">
<table>
<tr>
<td><input type="radio" name="submodel_prices" value="|9925|12400"> LX 4dr Sdn</td>
</tr>
<tr>
<td><input type="radio" name="submodel_prices" value="|11500|14200"> EX 4dr Sdn</td>
</tr>
<tr>
<td><input type="radio" name="submodel_prices" value="|13200|16100"> Touring 4dr Sdn</td>
</tr>
</table>
</form>
</body></html>`

	trims, err := parseVMRTrims(html)
	if err != nil {
		t.Fatalf("parseVMRTrims() error: %v", err)
	}
	if len(trims) != 3 {
		t.Fatalf("expected 3 trims, got %d", len(trims))
	}
	if trims[0].Wholesale != 9925 || trims[0].Retail != 12400 {
		t.Errorf("trim 0: wholesale=%v retail=%v, want 9925/12400", trims[0].Wholesale, trims[0].Retail)
	}
	if trims[1].Wholesale != 11500 {
		t.Errorf("trim 1: wholesale=%v, want 11500", trims[1].Wholesale)
	}
}

func TestParseVMRTable(t *testing.T) {
	// Simulates VMR's static table format used for older vehicles
	tableHTML := `<html><body>
<table>
<tr><th>Trim</th><th>Fair</th><th>Clean</th><th>Exc</th></tr>
<tr><td>S 4dr Sdn</td><td>1,375</td><td>3,375</td><td>5,450</td></tr>
<tr><td>Base 4dr Sdn</td><td>1,500</td><td>3,550</td><td>5,675</td></tr>
<tr><td>LX 4dr Sdn</td><td>1,650</td><td>3,725</td><td>5,975</td></tr>
</table>
</body></html>`

	trims := parseVMRTable(tableHTML)
	if len(trims) != 3 {
		t.Fatalf("expected 3 trims from table, got %d", len(trims))
	}
	if trims[0].Name != "S 4dr Sdn" {
		t.Errorf("trim 0 name = %q, want 'S 4dr Sdn'", trims[0].Name)
	}
	if trims[0].Wholesale != 1375 {
		t.Errorf("trim 0 wholesale = %v, want 1375", trims[0].Wholesale)
	}
	if trims[0].Retail != 3375 {
		t.Errorf("trim 0 retail = %v, want 3375", trims[0].Retail)
	}
	if trims[2].Name != "LX 4dr Sdn" {
		t.Errorf("trim 2 name = %q, want 'LX 4dr Sdn'", trims[2].Name)
	}
}

func TestParseVMRTableWithDollarSigns(t *testing.T) {
	tableHTML := `<html><body>
<table>
<tr><th>Trim</th><th>Fair</th><th>Clean</th><th>Exc</th></tr>
<tr><td>Base 4dr Sdn</td><td>$2,500</td><td>$4,500</td><td>$6,500</td></tr>
</table>
</body></html>`

	trims := parseVMRTable(tableHTML)
	if len(trims) != 1 {
		t.Fatalf("expected 1 trim, got %d", len(trims))
	}
	if trims[0].Wholesale != 2500 || trims[0].Retail != 4500 {
		t.Errorf("got wholesale=%v retail=%v, want 2500/4500", trims[0].Wholesale, trims[0].Retail)
	}
}

func TestParseVMRTableNoData(t *testing.T) {
	// A page with no pricing table should return nil
	noTableHTML := `<html><body><h1>Page Not Found</h1></body></html>`
	trims := parseVMRTable(noTableHTML)
	if len(trims) != 0 {
		t.Errorf("expected 0 trims from empty page, got %d", len(trims))
	}
}

func TestMatchTrim(t *testing.T) {
	trims := []vmrTrim{
		{Name: "LX 4dr Sdn", Wholesale: 9925, Retail: 12400},
		{Name: "EX 4dr Sdn", Wholesale: 11500, Retail: 14200},
		{Name: "Touring 4dr Sdn", Wholesale: 13200, Retail: 16100},
		{Name: "Si 2dr Cpe", Wholesale: 14000, Retail: 17000},
	}

	// Exact substring match
	got := matchTrim(trims, "EX")
	if got.Name != "EX 4dr Sdn" {
		t.Errorf("matchTrim('EX') = %q, want 'EX 4dr Sdn'", got.Name)
	}

	// Case-insensitive
	got = matchTrim(trims, "touring")
	if got.Name != "Touring 4dr Sdn" {
		t.Errorf("matchTrim('touring') = %q, want 'Touring 4dr Sdn'", got.Name)
	}

	// Fallback to cheapest when no match
	got = matchTrim(trims, "Sport")
	if got.Wholesale != 9925 {
		t.Errorf("matchTrim('Sport') should fall back to cheapest (9925), got %v", got.Wholesale)
	}
}

func TestCleanAlphaNum(t *testing.T) {
	if got := cleanAlphaNum("CR-V"); got != "CRV" {
		t.Errorf("cleanAlphaNum('CR-V') = %q, want 'CRV'", got)
	}
	if got := cleanAlphaNum("F-150 XLT"); got != "F150XLT" {
		t.Errorf("cleanAlphaNum('F-150 XLT') = %q, want 'F150XLT'", got)
	}
}
