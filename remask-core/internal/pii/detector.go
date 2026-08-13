package pii

import (
	"context"
	"sync"
)

type Detector interface {
	ID() string
	Detect(ctx context.Context, text string) ([]Entity, error)
}

type CompositeDetector struct {
	detectors []Detector
}

type EntityPolicy interface {
	EntityEnabled(entityType string) bool
}

type PolicyDetector struct {
	source Detector
	policy EntityPolicy
}

func NewPolicyDetector(source Detector, policy EntityPolicy) *PolicyDetector {
	return &PolicyDetector{source: source, policy: policy}
}

func (d *PolicyDetector) ID() string { return "entity-policy" }

func (d *PolicyDetector) Detect(ctx context.Context, text string) ([]Entity, error) {
	entities, err := d.source.Detect(ctx, text)
	if err != nil {
		return nil, err
	}
	filtered := make([]Entity, 0, len(entities))
	for _, entity := range entities {
		if isRuleEntity(entity) || d.policy.EntityEnabled(entity.Type) {
			filtered = append(filtered, entity)
		}
	}
	return filtered, nil
}

func isRuleEntity(entity Entity) bool {
	for _, source := range entity.Sources {
		if source == "rules" {
			return true
		}
	}
	return false
}

func NewCompositeDetector(detectors ...Detector) *CompositeDetector {
	return &CompositeDetector{detectors: detectors}
}

type DynamicDetector struct {
	base   []Detector
	mu     sync.RWMutex
	active Detector
}

func NewDynamicDetector(base ...Detector) *DynamicDetector {
	return &DynamicDetector{base: base}
}

func (d *DynamicDetector) ID() string { return "dynamic" }

func (d *DynamicDetector) Detect(ctx context.Context, text string) ([]Entity, error) {
	d.mu.RLock()
	detectors := append([]Detector(nil), d.base...)
	if d.active != nil {
		detectors = append(detectors, d.active)
	}
	d.mu.RUnlock()

	var entities []Entity
	for _, detector := range detectors {
		found, err := detector.Detect(ctx, text)
		if err != nil {
			return nil, err
		}
		entities = append(entities, found...)
	}
	return MergeEntities(entities), nil
}

func (d *DynamicDetector) Swap(active Detector) Detector {
	d.mu.Lock()
	previous := d.active
	d.active = active
	d.mu.Unlock()
	return previous
}

func (d *DynamicDetector) Active() Detector {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.active
}

func (d *CompositeDetector) ID() string { return "composite" }

func (d *CompositeDetector) Detect(ctx context.Context, text string) ([]Entity, error) {
	var entities []Entity
	for _, detector := range d.detectors {
		found, err := detector.Detect(ctx, text)
		if err != nil {
			return nil, err
		}
		entities = append(entities, found...)
	}
	return MergeEntities(entities), nil
}
