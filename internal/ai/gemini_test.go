package ai

import (
	"context"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type mockQuotaStore struct {
	quota *models.GeminiQuotaStatus
	err   error
}

func (m *mockQuotaStore) GetGeminiQuotaStatus(ctx context.Context) (*models.GeminiQuotaStatus, error) {
	return m.quota, m.err
}

func (m *mockQuotaStore) UpdateGeminiQuotaStatus(ctx context.Context, quota models.GeminiQuotaStatus) error {
	m.quota = &quota
	return m.err
}

func TestExtractJSONValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain JSON object",
			input: `{"clean_title": "Test", "is_warm": false, "is_lava_hot": false}`,
			want:  `{"clean_title": "Test", "is_warm": false, "is_lava_hot": false}`,
		},
		{
			name:  "JSON with trailing text",
			input: `{"clean_title": "Pioneer Subwoofer", "is_warm": false, "is_lava_hot": false}` + "\nThe eBay listing is overpriced.",
			want:  `{"clean_title": "Pioneer Subwoofer", "is_warm": false, "is_lava_hot": false}`,
		},
		{
			name:  "JSON array",
			input: `[{"item": "a"}, {"item": "b"}]`,
			want:  `[{"item": "a"}, {"item": "b"}]`,
		},
		{
			name:  "JSON array with trailing text",
			input: `[{"item": "a"}] some extra text`,
			want:  `[{"item": "a"}]`,
		},
		{
			name:  "JSON with escaped quotes",
			input: `{"title": "10\" Monitor", "ok": true}` + " trailing",
			want:  `{"title": "10\" Monitor", "ok": true}`,
		},
		{
			name:  "JSON with nested braces",
			input: `{"outer": {"inner": "val"}}` + " extra",
			want:  `{"outer": {"inner": "val"}}`,
		},
		{
			name:  "no JSON at all",
			input: `no json here`,
			want:  `no json here`,
		},
		{
			name:  "leading text before JSON",
			input: `Here is the result: {"ok": true}`,
			want:  `{"ok": true}`,
		},
		{
			name:  "braces inside strings should not confuse parser",
			input: `{"msg": "use {braces} carefully"}` + " done",
			want:  `{"msg": "use {braces} carefully"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSONValue(tt.input)
			if got != tt.want {
				t.Errorf("extractJSONValue(%q)\n  got:  %q\n  want: %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStripCodeBlockWithTrailingText(t *testing.T) {
	input := "```json\n{\"ok\": true}\n```\nSome explanation."
	got := stripCodeBlock(input)
	want := `{"ok": true}`
	if got != want {
		t.Errorf("stripCodeBlock with trailing text after code fence:\n  got:  %q\n  want: %q", got, want)
	}
}
