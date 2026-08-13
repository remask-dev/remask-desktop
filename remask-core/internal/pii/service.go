package pii

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/remask/remask-core/internal/scope"
)

var tokenPattern = regexp.MustCompile(`<([A-Z][A-Z0-9_]*):([A-F0-9]{4})>`)

type Service struct {
	detector Detector
	store    scope.Store
}

func NewService(detector Detector, store scope.Store) *Service {
	return &Service{detector: detector, store: store}
}

func (s *Service) Detect(ctx context.Context, text string) ([]Entity, error) {
	return s.detector.Detect(ctx, text)
}

func (s *Service) Redact(ctx context.Context, text string, scopeID string) (RedactResult, error) {
	entities, err := s.detector.Detect(ctx, text)
	if err != nil {
		return RedactResult{}, err
	}

	vault, err := s.resolveVault(ctx, scopeID)
	if err != nil {
		return RedactResult{}, err
	}

	redacted := text
	outputEntities := append([]Entity(nil), entities...)
	sort.Slice(outputEntities, func(i, j int) bool { return outputEntities[i].StartByte > outputEntities[j].StartByte })
	for index := range outputEntities {
		entity := &outputEntities[index]
		label := entity.Type
		if isRuleEntity(*entity) {
			label = "MASK_" + entity.Type
		}
		token, tokenErr := vault.TokenFor(label, entity.Text)
		if tokenErr != nil {
			return RedactResult{}, tokenErr
		}
		entity.Replacement = token
		entity.Text = ""
		redacted = redacted[:entity.StartByte] + token + redacted[entity.EndByte:]
	}
	sort.Slice(outputEntities, func(i, j int) bool { return outputEntities[i].StartByte < outputEntities[j].StartByte })

	return RedactResult{
		Text:             redacted,
		ScopeID:          vault.ID(),
		ExpiresAt:        vault.ExpiresAt().Format(time.RFC3339),
		ReplacementCount: len(outputEntities),
		Entities:         outputEntities,
	}, nil
}

func (s *Service) Restore(ctx context.Context, scopeID, text string) (RestoreResult, error) {
	vault, err := s.store.Get(ctx, scopeID)
	if err != nil {
		return RestoreResult{}, err
	}

	unknown := make([]string, 0)
	restoredCount := 0
	restored := tokenPattern.ReplaceAllStringFunc(text, func(token string) string {
		original, ok := vault.Resolve(token)
		if !ok {
			unknown = append(unknown, token)
			return token
		}
		restoredCount++
		return original
	})

	return RestoreResult{Text: restored, RestoredCount: restoredCount, UnknownTokens: uniqueStrings(unknown)}, nil
}

func (s *Service) Store() scope.Store { return s.store }

func (s *Service) resolveVault(ctx context.Context, scopeID string) (scope.Vault, error) {
	if strings.TrimSpace(scopeID) != "" {
		return s.store.Get(ctx, scopeID)
	}
	return s.store.Create(ctx, 0)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
