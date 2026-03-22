package facebook

import "testing"

func TestSanitizeJSONEscapes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no escapes",
			input: `{"price": 1000}`,
			want:  `{"price": 1000}`,
		},
		{
			name:  "valid escapes preserved",
			input: `{"msg": "line1\nline2"}`,
			want:  `{"msg": "line1\nline2"}`,
		},
		{
			name:  "invalid dollar escape removed",
			input: `{"desc": "price is \$18k"}`,
			want:  `{"desc": "price is $18k"}`,
		},
		{
			name:  "multiple invalid escapes",
			input: `{"a": "\$100", "b": "\$200"}`,
			want:  `{"a": "$100", "b": "$200"}`,
		},
		{
			name:  "mixed valid and invalid escapes",
			input: `{"a": "foo\nbar\$baz\\qux"}`,
			want:  `{"a": "foo\nbar$baz\\qux"}`,
		},
		{
			name:  "backslash outside string untouched",
			input: `{"key": "val"}`,
			want:  `{"key": "val"}`,
		},
		{
			name:  "escaped backslash before quote",
			input: `{"path": "C:\\", "next": "ok"}`,
			want:  `{"path": "C:\\", "next": "ok"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeJSONEscapes(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeJSONEscapes(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain JSON",
			input: `{"key": "val"}`,
			want:  `{"key": "val"}`,
		},
		{
			name:  "with markdown fence",
			input: "```json\n{\"key\": \"val\"}\n```",
			want:  `{"key": "val"}`,
		},
		{
			name:  "with preamble",
			input: "Here is the result:\n{\"key\": \"val\"}\nDone.",
			want:  `{"key": "val"}`,
		},
		{
			name:  "nested braces",
			input: `{"a": {"b": 1}}`,
			want:  `{"a": {"b": 1}}`,
		},
		{
			name:  "bare key-value pairs without braces",
			input: `"is_warm": false, "is_lava_hot": false, "title": "Test"`,
			want:  `{"is_warm": false, "is_lava_hot": false, "title": "Test"}`,
		},
		{
			name:  "preamble text then bare key-value pairs",
			input: "Some title text\n\"is_warm\": false, \"title\": \"Test\"",
			want:  `{"is_warm": false, "title": "Test"}`,
		},
		{
			name:  "bare key-value pairs with trailing comma",
			input: `"is_warm": false, "title": "Test",`,
			want:  `{"is_warm": false, "title": "Test"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
