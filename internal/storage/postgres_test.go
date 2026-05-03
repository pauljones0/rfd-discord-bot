package storage

import "testing"

func TestEncodeDecodeDocumentPrefersDocstoreTags(t *testing.T) {
	type sample struct {
		Name string `docstore:"displayName"`
		Skip string `docstore:"-"`
	}

	data, err := encodeDocument(sample{Name: "current", Skip: "hidden"})
	if err != nil {
		t.Fatalf("encodeDocument() error = %v", err)
	}
	if data["displayName"] != "current" {
		t.Fatalf("displayName = %v, want current", data["displayName"])
	}
	if _, ok := data["legacyName"]; ok {
		t.Fatalf("legacyName should not be encoded when docstore tag exists")
	}
	if _, ok := data["Skip"]; ok {
		t.Fatalf("Skip should not be encoded")
	}

	var out sample
	if err := decodeDocument(map[string]any{"displayName": "decoded"}, &out); err != nil {
		t.Fatalf("decodeDocument() error = %v", err)
	}
	if out.Name != "decoded" {
		t.Fatalf("Name = %q, want decoded", out.Name)
	}
}
