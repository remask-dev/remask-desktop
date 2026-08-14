package pii

import (
	"context"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/remask/remask-core/internal/scope"
)

var tokenPattern = regexp.MustCompile(`<([A-Z][A-Z0-9_]*):([A-F0-9]{4})>`)

type Service struct {
	detector    Detector
	store       scope.Store
	entityCache EntityCacheBackend
	logger      *log.Logger
}

type detectionResult struct {
	entities     []Entity
	cacheHit     bool
	cacheEnabled bool
	cacheState   string
	cacheError   error
}

func NewService(detector Detector, store scope.Store) *Service {
	service, err := NewServiceWithCache(detector, store, DefaultEntityCacheConfig())
	if err != nil {
		// The built-in defaults are validated, so this can only indicate a
		// programming error. Keep the historical constructor non-error based.
		panic(err)
	}
	return service
}

func NewServiceWithCache(detector Detector, store scope.Store, config EntityCacheConfig) (*Service, error) {
	cache, err := NewEntityCacheBackend(config)
	if err != nil {
		return nil, err
	}
	return NewServiceWithCacheBackend(detector, store, cache), nil
}

func NewServiceWithCacheBackend(detector Detector, store scope.Store, cache EntityCacheBackend) *Service {
	return &Service{detector: detector, store: store, entityCache: cache}
}

// SetLogger enables diagnostic redaction events. The logger is optional so
// callers that embed the PII service do not need to configure logging.
func (s *Service) SetLogger(logger *log.Logger) {
	s.logger = logger
}

func (s *Service) Detect(ctx context.Context, text string) ([]Entity, error) {
	result, err := s.detect(ctx, text)
	return result.entities, err
}

func (s *Service) detect(ctx context.Context, text string) (detectionResult, error) {
	result := detectionResult{cacheState: "miss"}
	if s.entityCache != nil {
		result.cacheEnabled = s.entityCache.Config().Enabled
		if !result.cacheEnabled {
			result.cacheState = "disabled"
		} else if entities, ok, err := s.entityCache.Lookup(ctx, text); err == nil && ok {
			result.entities = entities
			result.cacheHit = true
			result.cacheState = "hit"
			return result, nil
		} else if err != nil {
			// Cache failures are intentionally fail-open, but retaining the
			// error here makes the fallback visible in diagnostic logs.
			result.cacheError = err
			result.cacheState = "error"
		}
	}
	entities, err := s.detector.Detect(ctx, text)
	if err != nil {
		return result, err
	}
	// A cache outage must never prevent local PII detection. Redis and other
	// remote backends therefore fail open and are repopulated opportunistically.
	if s.entityCache != nil {
		_ = s.entityCache.Store(ctx, text, entities)
	}
	result.entities = cloneEntities(entities)
	return result, nil
}

func (s *Service) Redact(ctx context.Context, text string, scopeID string) (RedactResult, error) {
	started := time.Now()
	cacheState := "unknown"
	cacheHit := false
	cacheEnabled := false
	var cacheError error
	entityCount := 0
	resultStatus := "ok"
	defer func() {
		if s.logger == nil {
			return
		}
		elapsed := time.Since(started)
		// Keep this compatible with the standard library logger while making
		// the line easy to filter as a space-delimited structured event.
		s.logger.Printf("redact duration_ms=%.3f duration_us=%d cache_hit=%t cache_enabled=%t cache_state=%s entities=%d text_bytes=%d scope_reused=%t status=%s%v",
			float64(elapsed.Microseconds())/1000, elapsed.Microseconds(), cacheHit, cacheEnabled, cacheState,
			entityCount, len(text), strings.TrimSpace(scopeID) != "", resultStatus,
			cacheErrorLog(cacheError))
	}()

	detection, err := s.detect(ctx, text)
	cacheState, cacheHit, cacheEnabled, cacheError = detection.cacheState, detection.cacheHit, detection.cacheEnabled, detection.cacheError
	entities := detection.entities
	entityCount = len(entities)
	if err != nil {
		resultStatus = "error"
		return RedactResult{}, err
	}

	vault, err := s.resolveVault(ctx, scopeID)
	if err != nil {
		resultStatus = "error"
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
			resultStatus = "error"
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

func cacheErrorLog(err error) string {
	if err == nil {
		return ""
	}
	return " cache_error=" + err.Error()
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

func (s *Service) ConfigureEntityCache(config EntityCacheConfig) error {
	return s.entityCache.Configure(config)
}

func (s *Service) EntityCacheConfig() EntityCacheConfig {
	return s.entityCache.Config()
}

func (s *Service) ClearEntityCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.entityCache.Clear(ctx)
}

func (s *Service) Close() {
	_ = s.entityCache.Close()
}

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
