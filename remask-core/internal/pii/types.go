package pii

type Entity struct {
	Type        string   `json:"type"`
	Text        string   `json:"text,omitempty"`
	StartByte   int      `json:"start_byte"`
	EndByte     int      `json:"end_byte"`
	Confidence  float64  `json:"confidence"`
	Sources     []string `json:"sources"`
	Replacement string   `json:"replacement,omitempty"`
}

type RedactResult struct {
	Text             string   `json:"text"`
	ScopeID          string   `json:"scope_id"`
	ExpiresAt        string   `json:"expires_at"`
	ReplacementCount int      `json:"replacement_count"`
	Entities         []Entity `json:"entities"`
}

type RestoreResult struct {
	Text          string   `json:"text"`
	RestoredCount int      `json:"restored_count"`
	UnknownTokens []string `json:"unknown_tokens"`
}
