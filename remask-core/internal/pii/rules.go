package pii

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type RuleConfig struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Enabled bool   `json:"enabled"`
}

type EntityTypeConfig struct {
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

type PolicySettings struct {
	Enabled         bool               `json:"enabled"`
	RedactAIAnswers bool               `json:"redact_ai_answers"`
	EntityTypes     []EntityTypeConfig `json:"entity_types"`
	Rules           []RuleConfig       `json:"rules"`
}

type compiledRule struct {
	config  RuleConfig
	pattern *regexp.Regexp
}

type RuleDetector struct {
	mu     sync.RWMutex
	rules  []compiledRule
	policy PolicySettings
	path   string
}

func NewRuleDetector() *RuleDetector {
	detector, _ := NewRuleDetectorWithDataDir("")
	return detector
}

func DefaultPolicySettings() PolicySettings {
	return PolicySettings{Enabled: true, EntityTypes: []EntityTypeConfig{
		{Type: "ACCOUNT_NUMBER", Enabled: true},
		{Type: "ADDRESS", Enabled: true},
		{Type: "EMAIL_ADDRESS", Enabled: true},
		{Type: "IP_ADDRESS", Enabled: true},
		{Type: "PERSON", Enabled: true},
		{Type: "PHONE_NUMBER", Enabled: true},
		{Type: "PRIVATE_DATE", Enabled: true},
		{Type: "SECRET", Enabled: true},
		{Type: "URL", Enabled: true},
	}, Rules: []RuleConfig{
		{ID: "EMAIL", Pattern: `(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`, Enabled: true},
	}}
}

func NewRuleDetectorWithDataDir(dataDir string) (*RuleDetector, error) {
	d := &RuleDetector{policy: DefaultPolicySettings()}
	if dataDir != "" {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, err
		}
		d.path = filepath.Join(dataDir, "policy.json")
		if data, err := os.ReadFile(d.path); err == nil {
			if err := json.Unmarshal(data, &d.policy); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	compiled, err := compilePolicy(d.policy)
	if err != nil {
		return nil, err
	}
	d.rules = compiled
	return d, nil
}

func (d *RuleDetector) ID() string { return "rules" }

func (d *RuleDetector) Detect(ctx context.Context, text string) ([]Entity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	d.mu.RLock()
	rules := append([]compiledRule(nil), d.rules...)
	d.mu.RUnlock()
	var entities []Entity
	for _, current := range rules {
		if !current.config.Enabled {
			continue
		}
		for _, match := range current.pattern.FindAllStringIndex(text, -1) {
			entities = append(entities, Entity{
				Type:       strings.ToUpper(current.config.ID),
				Text:       text[match[0]:match[1]],
				StartByte:  match[0],
				EndByte:    match[1],
				Confidence: 1,
				Sources:    []string{d.ID()},
			})
		}
	}
	return MergeEntities(entities), nil
}

func (d *RuleDetector) Policy() PolicySettings {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := d.policy
	result.EntityTypes = append([]EntityTypeConfig(nil), d.policy.EntityTypes...)
	result.Rules = append([]RuleConfig(nil), d.policy.Rules...)
	return result
}

func (d *RuleDetector) Enabled() bool { d.mu.RLock(); defer d.mu.RUnlock(); return d.policy.Enabled }

func (d *RuleDetector) RedactAIAnswers() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.policy.RedactAIAnswers
}

func (d *RuleDetector) EntityEnabled(entityType string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, item := range d.policy.EntityTypes {
		if item.Type == entityType {
			return item.Enabled
		}
	}
	return true
}

func (d *RuleDetector) Configure(policy PolicySettings) error {
	compiled, err := compilePolicy(policy)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.policy = policy
	d.rules = compiled
	d.mu.Unlock()
	if d.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	temporary := d.path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, d.path)
}

func compilePolicy(policy PolicySettings) ([]compiledRule, error) {
	seenEntityTypes := map[string]bool{}
	for _, item := range policy.EntityTypes {
		if item.Type == "" {
			return nil, errors.New("entity type is required")
		}
		if seenEntityTypes[item.Type] {
			return nil, errors.New("entity types must be unique")
		}
		seenEntityTypes[item.Type] = true
	}
	if len(policy.Rules) > 100 {
		return nil, errors.New("at most 100 rules are allowed")
	}
	compiled := make([]compiledRule, 0, len(policy.Rules))
	seen := map[string]bool{}
	for _, item := range policy.Rules {
		if item.ID == "" || item.Pattern == "" {
			return nil, errors.New("rule id and pattern are required")
		}
		if !validRuleID(item.ID) {
			return nil, errors.New("rule id may only contain letters, numbers, underscore, dot, and hyphen")
		}
		if seen[item.ID] {
			return nil, errors.New("rule ids must be unique")
		}
		seen[item.ID] = true
		pattern, err := regexp.Compile(item.Pattern)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, compiledRule{config: item, pattern: pattern})
	}
	return compiled, nil
}

func validRuleID(value string) bool {
	for _, character := range value {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return value != ""
}
