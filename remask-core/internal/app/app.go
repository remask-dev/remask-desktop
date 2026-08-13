package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/remask/remask-core/internal/audit"
	"github.com/remask/remask-core/internal/gateway"
	"github.com/remask/remask-core/internal/httpapi"
	"github.com/remask/remask-core/internal/model"
	"github.com/remask/remask-core/internal/operation"
	"github.com/remask/remask-core/internal/pii"
	"github.com/remask/remask-core/internal/profile"
	"github.com/remask/remask-core/internal/scope"
	"github.com/remask/remask-core/internal/upstream"
)

type App struct {
	handler      http.Handler
	proxyHandler http.Handler
}

func New(logger *log.Logger) (*App, error) {
	return NewWithOptions(logger, Options{})
}

func NewWithHTTPClient(logger *log.Logger, httpClient *http.Client) (*App, error) {
	return NewWithOptions(logger, Options{HTTPClient: httpClient})
}

type Options struct {
	HTTPClient  *http.Client
	ModelsDir   string
	Runtime     model.Runtime
	ActiveModel string
	DeviceKey   []byte
	DataDir     string
}

func NewWithOptions(logger *log.Logger, options Options) (*App, error) {
	store, err := scope.NewMemoryStore(15*time.Minute, options.DeviceKey)
	if err != nil {
		return nil, fmt.Errorf("initialize entity store: %w", err)
	}
	rules, err := pii.NewRuleDetectorWithDataDir(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("initialize policy rules: %w", err)
	}
	detector := pii.NewDynamicDetector(rules)
	service := pii.NewService(pii.NewPolicyDetector(detector, rules), store)
	operations := operation.NewStore()
	modelsDir := options.ModelsDir
	if modelsDir == "" {
		modelsDir = os.Getenv("REMASK_MODELS_DIR")
	}
	if modelsDir == "" {
		modelsDir = "models"
	}
	models := model.NewManager(modelsDir, options.Runtime, detector, operations)
	if options.ActiveModel != "" {
		if _, err := models.Scan(context.Background()); err != nil {
			return nil, fmt.Errorf("scan models: %w", err)
		}
		if err := models.ActivateSync(context.Background(), options.ActiveModel); err != nil {
			return nil, fmt.Errorf("activate model %q: %w", options.ActiveModel, err)
		}
	}

	profiles := profile.NewRegistry(profile.Builtins()...)
	upstreams, err := upstream.NewRegistry(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("initialize upstream registry: %w", err)
	}
	audits, err := audit.NewStore(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("initialize audit store: %w", err)
	}
	proxy := gateway.New(logger, upstreams, profiles, service, audits, options.HTTPClient, rules)

	handler := httpapi.NewRouter(logger, service, profiles, upstreams, models, operations, audits, rules)
	return &App{handler: handler, proxyHandler: httpapi.NewProxyRouter(logger, proxy)}, nil
}

func (a *App) Handler() http.Handler {
	return a.handler
}

func (a *App) ProxyHandler() http.Handler {
	return a.proxyHandler
}
