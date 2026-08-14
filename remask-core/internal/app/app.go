package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/remask/remask-core/internal/audit"
	"github.com/remask/remask-core/internal/forwardproxy"
	"github.com/remask/remask-core/internal/gateway"
	"github.com/remask/remask-core/internal/httpapi"
	"github.com/remask/remask-core/internal/mitm"
	"github.com/remask/remask-core/internal/model"
	"github.com/remask/remask-core/internal/operation"
	"github.com/remask/remask-core/internal/pii"
	"github.com/remask/remask-core/internal/profile"
	"github.com/remask/remask-core/internal/scope"
	"github.com/remask/remask-core/internal/upstream"
)

type App struct {
	handler             http.Handler
	proxyHandler        http.Handler
	forwardProxyHandler http.Handler
	proxyAuthority      *mitm.Authority
}

func New(logger *log.Logger) (*App, error) {
	return NewWithOptions(logger, Options{})
}

func NewWithHTTPClient(logger *log.Logger, httpClient *http.Client) (*App, error) {
	return NewWithOptions(logger, Options{HTTPClient: httpClient})
}

type Options struct {
	HTTPClient             *http.Client
	ModelsDir              string
	Runtime                model.Runtime
	ActiveModel            string
	DataDir                string
	EntityCacheBackend     string
	EntityCacheRedisURL    string
	EntityCacheRedisPrefix string
}

func NewWithOptions(logger *log.Logger, options Options) (*App, error) {
	// Entity label suffixes are deterministic from the entity itself and do not
	// depend on a machine or device identifier.
	store, err := scope.NewMemoryStore(15 * time.Minute)
	if err != nil {
		return nil, fmt.Errorf("initialize entity store: %w", err)
	}
	audits, err := audit.NewStore(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("initialize audit store: %w", err)
	}
	rules, err := pii.NewRuleDetectorWithDataDir(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("initialize policy rules: %w", err)
	}
	detector := pii.NewDynamicDetector(rules)
	settings := audits.Settings()
	cacheBackend := options.EntityCacheBackend
	if cacheBackend == "" {
		cacheBackend = os.Getenv("REMASK_ENTITY_CACHE_BACKEND")
	}
	redisURL := options.EntityCacheRedisURL
	if redisURL == "" {
		redisURL = os.Getenv("REMASK_ENTITY_CACHE_REDIS_URL")
	}
	redisPrefix := options.EntityCacheRedisPrefix
	if redisPrefix == "" {
		redisPrefix = os.Getenv("REMASK_ENTITY_CACHE_REDIS_PREFIX")
	}
	service, err := pii.NewServiceWithCache(pii.NewPolicyDetector(detector, rules), store, pii.EntityCacheConfig{
		Enabled:   settings.EntityCacheEnabled,
		TTL:       time.Duration(settings.EntityCacheTTLSeconds) * time.Second,
		Backend:   cacheBackend,
		RedisURL:  redisURL,
		KeyPrefix: redisPrefix,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize entity cache: %w", err)
	}
	service.SetLogger(logger)
	operations := operation.NewStore()
	modelsDir := options.ModelsDir
	if modelsDir == "" {
		modelsDir = os.Getenv("REMASK_MODELS_DIR")
	}
	if modelsDir == "" {
		modelsDir = "models"
	}
	models := model.NewManager(modelsDir, options.Runtime, detector, operations)
	models.SetMaxInferenceTokens(audits.Settings().MaxInferenceTokens)
	if err := models.SetProvider(audits.Settings().InferenceProvider); err != nil {
		return nil, fmt.Errorf("configure model provider: %w", err)
	}
	if options.ActiveModel != "" {
		if _, err := models.Scan(context.Background()); err != nil {
			return nil, fmt.Errorf("scan models: %w", err)
		}
		if err := models.ActivateSync(context.Background(), options.ActiveModel); err != nil {
			if errors.Is(err, model.ErrNotFound) {
				logger.Printf("active model is not installed; continuing with rules only model=%q", options.ActiveModel)
			} else {
				return nil, fmt.Errorf("activate model %q: %w", options.ActiveModel, err)
			}
		}
	}

	profiles := profile.NewRegistry(profile.Builtins()...)
	upstreams, err := upstream.NewRegistry(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("initialize upstream registry: %w", err)
	}
	authority, err := mitm.NewAuthority(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("initialize local proxy certificate authority: %w", err)
	}
	proxy := gateway.New(logger, upstreams, profiles, service, audits, options.HTTPClient, rules)
	forward := forwardproxy.New(logger, upstreams, proxy, authority)

	handler := httpapi.NewRouter(logger, service, profiles, upstreams, models, operations, audits, authority, rules)
	return &App{
		handler: handler, proxyHandler: httpapi.NewProxyRouter(logger, proxy),
		forwardProxyHandler: forward, proxyAuthority: authority,
	}, nil
}

func (a *App) Handler() http.Handler {
	return a.handler
}

func (a *App) ProxyHandler() http.Handler {
	return a.proxyHandler
}

func (a *App) ForwardProxyHandler() http.Handler { return a.forwardProxyHandler }

func (a *App) ProxyAuthorityStatus() mitm.Status { return a.proxyAuthority.Status() }

func (a *App) RootCertificatePEM() []byte { return a.proxyAuthority.RootCertificatePEM() }
