package document

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

var ErrInvalidSelector = errors.New("invalid selector")

type Transformer struct{}

type TextMatch struct {
	Path  string
	Value string
}

type ScalarMatch struct {
	Path  string
	Value string
}

func NewTransformer() *Transformer { return &Transformer{} }

func (t *Transformer) TransformJSON(body []byte, selectors []string, transform func(string) (string, error)) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}

	for _, selector := range selectors {
		segments, err := parseSelector(selector)
		if err != nil {
			return nil, err
		}
		if err := transformAt(&document, segments, transform); err != nil {
			return nil, fmt.Errorf("selector %q: %w", selector, err)
		}
	}

	return encodeJSON(document)
}

func (t *Transformer) TransformJSONMatches(body []byte, selectors []string, transform func(TextMatch) (string, error)) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}

	for _, selector := range selectors {
		segments, err := parseSelector(selector)
		if err != nil {
			return nil, err
		}
		if err := transformAtPath(&document, segments, nil, transform); err != nil {
			return nil, fmt.Errorf("selector %q: %w", selector, err)
		}
	}
	return encodeJSON(document)
}

func (t *Transformer) ExtractStrings(body []byte, selectors []string) ([]TextMatch, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}

	var matches []TextMatch
	for _, selector := range selectors {
		segments, err := parseSelector(selector)
		if err != nil {
			return nil, err
		}
		if err := visitAtPath(&document, segments, nil, func(match TextMatch) error {
			matches = append(matches, match)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func (t *Transformer) ExtractScalars(body []byte, selectors []string) ([]ScalarMatch, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}

	var matches []ScalarMatch
	for _, selector := range selectors {
		segments, err := parseSelector(selector)
		if err != nil {
			return nil, err
		}
		if err := visitScalarAtPath(&document, segments, nil, func(match ScalarMatch) error {
			matches = append(matches, match)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func encodeJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n")), nil
}

func ensureEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not supported")
		}
		return fmt.Errorf("decode trailing json: %w", err)
	}
	return nil
}

func parseSelector(selector string) ([]string, error) {
	if selector == "" {
		return []string{}, nil
	}
	if !strings.HasPrefix(selector, "/") {
		return nil, ErrInvalidSelector
	}
	parts := strings.Split(selector[1:], "/")
	for index := range parts {
		parts[index] = strings.ReplaceAll(strings.ReplaceAll(parts[index], "~1", "/"), "~0", "~")
	}
	return parts, nil
}

func transformAt(node *any, segments []string, transform func(string) (string, error)) error {
	if len(segments) == 0 {
		text, ok := (*node).(string)
		if !ok {
			return nil
		}
		result, err := transform(text)
		if err != nil {
			return err
		}
		*node = result
		return nil
	}

	head, tail := segments[0], segments[1:]
	switch current := (*node).(type) {
	case map[string]any:
		if head == "*" {
			for _, key := range sortedKeys(current) {
				child := current[key]
				value := child
				if err := transformAt(&value, tail, transform); err != nil {
					return err
				}
				current[key] = value
			}
			return nil
		}
		child, ok := current[head]
		if !ok {
			return nil
		}
		if err := transformAt(&child, tail, transform); err != nil {
			return err
		}
		current[head] = child
	case []any:
		if head == "*" {
			for index := range current {
				child := current[index]
				if err := transformAt(&child, tail, transform); err != nil {
					return err
				}
				current[index] = child
			}
			return nil
		}
		index, err := strconv.Atoi(head)
		if err != nil || index < 0 || index >= len(current) {
			return nil
		}
		child := current[index]
		if err := transformAt(&child, tail, transform); err != nil {
			return err
		}
		current[index] = child
	}
	return nil
}

func transformAtPath(node *any, segments, currentPath []string, transform func(TextMatch) (string, error)) error {
	if len(segments) == 0 {
		text, ok := (*node).(string)
		if !ok {
			return nil
		}
		result, err := transform(TextMatch{Path: "/" + strings.Join(currentPath, "/"), Value: text})
		if err != nil {
			return err
		}
		*node = result
		return nil
	}

	head, tail := segments[0], segments[1:]
	switch current := (*node).(type) {
	case map[string]any:
		if head == "*" {
			for _, key := range sortedKeys(current) {
				child := current[key]
				value := child
				if err := transformAtPath(&value, tail, appendPath(currentPath, escapePathSegment(key)), transform); err != nil {
					return err
				}
				current[key] = value
			}
			return nil
		}
		child, ok := current[head]
		if !ok {
			return nil
		}
		if err := transformAtPath(&child, tail, appendPath(currentPath, escapePathSegment(head)), transform); err != nil {
			return err
		}
		current[head] = child
	case []any:
		if head == "*" {
			for index := range current {
				child := current[index]
				if err := transformAtPath(&child, tail, appendPath(currentPath, strconv.Itoa(index)), transform); err != nil {
					return err
				}
				current[index] = child
			}
			return nil
		}
		index, err := strconv.Atoi(head)
		if err != nil || index < 0 || index >= len(current) {
			return nil
		}
		child := current[index]
		if err := transformAtPath(&child, tail, appendPath(currentPath, strconv.Itoa(index)), transform); err != nil {
			return err
		}
		current[index] = child
	}
	return nil
}

func visitAtPath(node *any, segments, currentPath []string, visit func(TextMatch) error) error {
	if len(segments) == 0 {
		text, ok := (*node).(string)
		if !ok {
			return nil
		}
		return visit(TextMatch{Path: "/" + strings.Join(currentPath, "/"), Value: text})
	}

	head, tail := segments[0], segments[1:]
	switch current := (*node).(type) {
	case map[string]any:
		if head == "*" {
			for _, key := range sortedKeys(current) {
				child := current[key]
				value := child
				if err := visitAtPath(&value, tail, appendPath(currentPath, escapePathSegment(key)), visit); err != nil {
					return err
				}
			}
			return nil
		}
		child, ok := current[head]
		if !ok {
			return nil
		}
		return visitAtPath(&child, tail, appendPath(currentPath, escapePathSegment(head)), visit)
	case []any:
		if head == "*" {
			for index, child := range current {
				value := child
				if err := visitAtPath(&value, tail, appendPath(currentPath, strconv.Itoa(index)), visit); err != nil {
					return err
				}
			}
			return nil
		}
		index, err := strconv.Atoi(head)
		if err != nil || index < 0 || index >= len(current) {
			return nil
		}
		child := current[index]
		return visitAtPath(&child, tail, appendPath(currentPath, strconv.Itoa(index)), visit)
	}
	return nil
}

func visitScalarAtPath(node *any, segments, currentPath []string, visit func(ScalarMatch) error) error {
	if len(segments) == 0 {
		var value string
		switch scalar := (*node).(type) {
		case string:
			value = scalar
		case json.Number:
			value = scalar.String()
		case bool:
			value = strconv.FormatBool(scalar)
		default:
			return nil
		}
		return visit(ScalarMatch{Path: "/" + strings.Join(currentPath, "/"), Value: value})
	}

	head, tail := segments[0], segments[1:]
	switch current := (*node).(type) {
	case map[string]any:
		if head == "*" {
			for _, key := range sortedKeys(current) {
				child := current[key]
				value := child
				if err := visitScalarAtPath(&value, tail, appendPath(currentPath, escapePathSegment(key)), visit); err != nil {
					return err
				}
			}
			return nil
		}
		child, ok := current[head]
		if !ok {
			return nil
		}
		return visitScalarAtPath(&child, tail, appendPath(currentPath, escapePathSegment(head)), visit)
	case []any:
		if head == "*" {
			for index, child := range current {
				value := child
				if err := visitScalarAtPath(&value, tail, appendPath(currentPath, strconv.Itoa(index)), visit); err != nil {
					return err
				}
			}
			return nil
		}
		index, err := strconv.Atoi(head)
		if err != nil || index < 0 || index >= len(current) {
			return nil
		}
		child := current[index]
		return visitScalarAtPath(&child, tail, appendPath(currentPath, strconv.Itoa(index)), visit)
	}
	return nil
}

func appendPath(path []string, segment string) []string {
	result := make([]string, len(path), len(path)+1)
	copy(result, path)
	return append(result, segment)
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func escapePathSegment(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
