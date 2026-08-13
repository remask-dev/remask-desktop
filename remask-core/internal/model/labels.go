package model

import (
	"encoding/json"
	"fmt"
	"os"
)

func readLabelMap(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var direct map[string]string
	if err := json.Unmarshal(data, &direct); err == nil && len(direct) > 0 {
		return direct, nil
	}
	var wrapped struct {
		IDToLabel map[string]string `json:"id2label"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && len(wrapped.IDToLabel) > 0 {
		return wrapped.IDToLabel, nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil && len(list) > 0 {
		result := make(map[string]string, len(list))
		for index, label := range list {
			result[fmt.Sprint(index)] = label
		}
		return result, nil
	}
	return nil, fmt.Errorf("labels file must be an id-to-label object, an id2label object, or an array")
}
