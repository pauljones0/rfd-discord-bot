package models

// GroundingSource identifies a web source returned by Gemini grounding.
type GroundingSource struct {
	Title  string `json:"title,omitempty" docstore:"title,omitempty"`
	URI    string `json:"uri,omitempty" docstore:"uri,omitempty"`
	Domain string `json:"domain,omitempty" docstore:"domain,omitempty"`
}

// GenerationMetadata captures non-text metadata returned by a Gemini call.
type GenerationMetadata struct {
	Grounded         bool              `json:"grounded,omitempty" docstore:"grounded,omitempty"`
	GroundingSources []GroundingSource `json:"grounding_sources,omitempty" docstore:"grounding_sources,omitempty"`
	WebSearchQueries []string          `json:"web_search_queries,omitempty" docstore:"web_search_queries,omitempty"`
}
