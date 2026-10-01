package notifier

import (
	"encoding/json"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"os"
	"strings"
	"testing"
)

func TestExportRustRenderParity(t *testing.T) {
	if os.Getenv("RUST_PARITY_OUTPUT") == "" {
		t.Skip("offline golden export requires RUST_PARITY_OUTPUT")
	}
	b, e := os.ReadFile(os.Getenv("RUST_PARITY_INPUT"))
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Deal models.DealInfo `json:"deal"`
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	var out []any
	for _, c := range cases {
		out = append(out, map[string]any{"deal": c.Deal, "payload": createDiscordPayload(c.Deal), "nonce": messageNonce("1001", "42", c.Deal)})
	}
	d := cases[0].Deal
	d.Title = strings.Repeat("🌳", 180)
	d.CleanTitle = ""
	out = append(out, map[string]any{"deal": d, "payload": createDiscordPayload(d), "nonce": messageNonce("1001", "42", d)})
	b, e = json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(os.Getenv("RUST_PARITY_OUTPUT"), b, 0600); e != nil {
		t.Fatal(e)
	}
}
