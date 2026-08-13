package modeldownload

import (
	_ "embed"
	"encoding/json"
)

type CatalogEntry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ProjectURL string `json:"project_url"`
	Repo       string `json:"repo"`
	Revision   string `json:"revision"`
	Variant    string `json:"variant"`
}

//go:embed catalog.json
var catalogJSON []byte

func Catalog() []CatalogEntry {
	var entries []CatalogEntry
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		return []CatalogEntry{}
	}
	return entries
}
