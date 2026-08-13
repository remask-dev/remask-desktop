package pii

import "sort"

func MergeEntities(entities []Entity) []Entity {
	if len(entities) == 0 {
		return []Entity{}
	}

	sort.SliceStable(entities, func(i, j int) bool {
		if entities[i].StartByte != entities[j].StartByte {
			return entities[i].StartByte < entities[j].StartByte
		}
		if entities[i].EndByte != entities[j].EndByte {
			return entities[i].EndByte > entities[j].EndByte
		}
		return entities[i].Confidence > entities[j].Confidence
	})

	merged := make([]Entity, 0, len(entities))
	for _, candidate := range entities {
		if len(merged) == 0 {
			merged = append(merged, candidate)
			continue
		}

		last := &merged[len(merged)-1]
		if candidate.StartByte == last.StartByte && candidate.EndByte == last.EndByte && candidate.Type == last.Type {
			last.Sources = mergeStrings(last.Sources, candidate.Sources)
			if candidate.Confidence > last.Confidence {
				last.Confidence = candidate.Confidence
			}
			continue
		}

		if candidate.StartByte < last.EndByte {
			continue
		}
		merged = append(merged, candidate)
	}
	return merged
}

func mergeStrings(left, right []string) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	result := make([]string, 0, len(left)+len(right))
	for _, values := range [][]string{left, right} {
		for _, value := range values {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}
