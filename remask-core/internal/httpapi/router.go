package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/remask/remask-core/internal/audit"
	"github.com/remask/remask-core/internal/gateway"
	"github.com/remask/remask-core/internal/model"
	"github.com/remask/remask-core/internal/modeldownload"
	"github.com/remask/remask-core/internal/operation"
	"github.com/remask/remask-core/internal/pii"
	"github.com/remask/remask-core/internal/profile"
	"github.com/remask/remask-core/internal/scope"
	"github.com/remask/remask-core/internal/upstream"
)

const maxAPIBodyBytes = 2 << 20

type Router struct {
	logger     *log.Logger
	pii        *pii.Service
	profiles   *profile.Registry
	upstreams  *upstream.Registry
	models     *model.Manager
	operations *operation.Store
	audits     *audit.Store
	rules      *pii.RuleDetector
	startedAt  time.Time
}

func NewRouter(logger *log.Logger, service *pii.Service, profiles *profile.Registry, upstreams *upstream.Registry, models *model.Manager, operations *operation.Store, audits *audit.Store, configuredRules ...*pii.RuleDetector) http.Handler {
	rules := pii.NewRuleDetector()
	if len(configuredRules) > 0 && configuredRules[0] != nil {
		rules = configuredRules[0]
	}
	router := &Router{
		logger: logger, pii: service, profiles: profiles, upstreams: upstreams, models: models, operations: operations, audits: audits, rules: rules,
		startedAt: time.Now().UTC(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", router.health)
	mux.HandleFunc("GET /api/v1/ready", router.ready)
	mux.HandleFunc("GET /api/v1/version", router.version)
	mux.HandleFunc("POST /api/v1/detect", router.detect)
	mux.HandleFunc("POST /api/v1/redact", router.redact)
	mux.HandleFunc("POST /api/v1/restore", router.restore)
	mux.HandleFunc("GET /api/v1/scopes/{scope_id}", router.getScope)
	mux.HandleFunc("DELETE /api/v1/scopes/{scope_id}", router.deleteScope)
	mux.HandleFunc("GET /api/v1/profiles", router.listProfiles)
	mux.HandleFunc("GET /api/v1/upstreams", router.listUpstreams)
	mux.HandleFunc("POST /api/v1/upstreams", router.putUpstream)
	mux.HandleFunc("GET /api/v1/upstreams/{upstream_id}", router.getUpstream)
	mux.HandleFunc("PUT /api/v1/upstreams/{upstream_id}", router.putUpstream)
	mux.HandleFunc("DELETE /api/v1/upstreams/{upstream_id}", router.deleteUpstream)
	mux.HandleFunc("GET /api/v1/models", router.listModels)
	mux.HandleFunc("POST /api/v1/models/scan", router.scanModels)
	mux.HandleFunc("GET /api/v1/models/catalog", router.modelCatalog)
	mux.HandleFunc("POST /api/v1/models/download", router.downloadModel)
	mux.HandleFunc("GET /api/v1/models/active", router.activeModel)
	mux.HandleFunc("GET /api/v1/models/{model_id}", router.getModel)
	mux.HandleFunc("POST /api/v1/models/{model_id}/activate", router.activateModel)
	mux.HandleFunc("POST /api/v1/models/{model_id}/unload", router.unloadModel)
	mux.HandleFunc("GET /api/v1/operations", router.listOperations)
	mux.HandleFunc("GET /api/v1/operations/{operation_id}", router.getOperation)
	mux.HandleFunc("DELETE /api/v1/operations/{operation_id}", router.cancelOperation)
	mux.HandleFunc("GET /api/v1/audit/logs", router.listAuditLogs)
	mux.HandleFunc("DELETE /api/v1/audit/logs", router.clearAuditLogs)
	mux.HandleFunc("GET /api/v1/audit/stats", router.auditStats)
	mux.HandleFunc("GET /api/v1/settings", router.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", router.putSettings)
	mux.HandleFunc("GET /api/v1/policy", router.getPolicy)
	mux.HandleFunc("PUT /api/v1/policy", router.putPolicy)
	return requestMiddleware(logger, mux)
}

func (r *Router) getPolicy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, r.rules.Policy())
}
func (r *Router) putPolicy(w http.ResponseWriter, request *http.Request) {
	var policy pii.PolicySettings
	if !decodeJSON(w, request, &policy) {
		return
	}
	if err := r.rules.Configure(policy); err != nil {
		writeError(w, http.StatusBadRequest, "POLICY_INVALID", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, r.rules.Policy())
}

func NewProxyRouter(logger *log.Logger, proxy *gateway.Gateway) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", proxy)
	return requestMiddleware(logger, mux)
}

func (r *Router) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "uptime_seconds": int(time.Since(r.startedAt).Seconds())})
}

func (r *Router) ready(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (r *Router) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": "remask-core", "version": "0.1.0-dev", "api_version": "v1",
		"capabilities":  []string{"pii.rules", "pii.rules.configurable", "pii.entity-toggle", "pii.redact", "pii.restore", "proxy.http-json", "proxy.sse", "proxy.service-id-route", "proxy.domain-route", "proxy.auto-route", "proxy.path-passthrough", "proxy.global-toggle", "models.manifest", "models.hot-swap", "audit.sqlite", "audit.masked-log", "audit.token-usage", "audit.stats", "settings.persisted", "upstreams.persisted"},
		"model_runtime": r.models.RuntimeStatus(),
	})
}

type detectRequest struct {
	Text     string `json:"text"`
	PolicyID string `json:"policy_id,omitempty"`
}

func (r *Router) detect(w http.ResponseWriter, request *http.Request) {
	var input detectRequest
	if !decodeJSON(w, request, &input) {
		return
	}
	start := time.Now()
	entities, err := r.pii.Detect(request.Context(), input.Text)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "DETECT_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": entities, "processing_ms": time.Since(start).Milliseconds()})
}

type scopeRequest struct {
	Mode string `json:"mode,omitempty"`
	ID   string `json:"id,omitempty"`
}

type redactRequest struct {
	Text     string       `json:"text"`
	PolicyID string       `json:"policy_id,omitempty"`
	Scope    scopeRequest `json:"scope,omitempty"`
}

func (r *Router) redact(w http.ResponseWriter, request *http.Request) {
	var input redactRequest
	if !decodeJSON(w, request, &input) {
		return
	}
	result, err := r.pii.Redact(request.Context(), input.Text, input.Scope.ID)
	if err != nil {
		status := http.StatusUnprocessableEntity
		code := "REDACT_FAILED"
		if errors.Is(err, scope.ErrNotFound) {
			status, code = http.StatusNotFound, "SCOPE_NOT_FOUND"
		}
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type restoreRequest struct {
	ScopeID string `json:"scope_id"`
	Text    string `json:"text"`
}

func (r *Router) restore(w http.ResponseWriter, request *http.Request) {
	var input restoreRequest
	if !decodeJSON(w, request, &input) {
		return
	}
	if input.ScopeID == "" {
		writeError(w, http.StatusBadRequest, "SCOPE_REQUIRED", "scope_id is required")
		return
	}
	result, err := r.pii.Restore(request.Context(), input.ScopeID, input.Text)
	if err != nil {
		status := http.StatusUnprocessableEntity
		code := "RESTORE_FAILED"
		if errors.Is(err, scope.ErrNotFound) {
			status, code = http.StatusNotFound, "SCOPE_NOT_FOUND"
		}
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (r *Router) getScope(w http.ResponseWriter, request *http.Request) {
	vault, err := r.pii.Store().Get(request.Context(), request.PathValue("scope_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "SCOPE_NOT_FOUND", "scope was not found or expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": vault.ID(), "expires_at": vault.ExpiresAt().Format(time.RFC3339)})
}

func (r *Router) deleteScope(w http.ResponseWriter, request *http.Request) {
	if err := r.pii.Store().Delete(request.Context(), request.PathValue("scope_id")); err != nil {
		writeError(w, http.StatusInternalServerError, "SCOPE_DELETE_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Router) listProfiles(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"profiles": r.profiles.List()})
}

func (r *Router) listUpstreams(w http.ResponseWriter, _ *http.Request) {
	items := r.upstreams.List()
	for index := range items {
		items[index] = items[index].Public()
	}
	writeJSON(w, http.StatusOK, map[string]any{"upstreams": items})
}

func (r *Router) getUpstream(w http.ResponseWriter, request *http.Request) {
	item, err := r.upstreams.Get(request.PathValue("upstream_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "UPSTREAM_NOT_FOUND", "upstream is not configured")
		return
	}
	writeJSON(w, http.StatusOK, item.Public())
}

func (r *Router) putUpstream(w http.ResponseWriter, request *http.Request) {
	var item upstream.Upstream
	if !decodeJSON(w, request, &item) {
		return
	}
	pathID := request.PathValue("upstream_id")
	if pathID != "" {
		if item.ID != "" && item.ID != pathID {
			writeError(w, http.StatusBadRequest, "UPSTREAM_ID_MISMATCH", "body id must match path id")
			return
		}
		item.ID = pathID
		if item.CredentialMode == "managed" && (item.APIKey == "" || item.APIKey == "••••••••") {
			if existing, err := r.upstreams.Get(pathID); err == nil {
				item.APIKey = existing.APIKey
			}
		}
	}
	if _, ok := r.profiles.Get(item.ProfileID); !ok {
		writeError(w, http.StatusBadRequest, "PROFILE_NOT_FOUND", "profile_id is not configured")
		return
	}
	if err := r.upstreams.Put(item); err != nil {
		writeError(w, http.StatusBadRequest, "UPSTREAM_INVALID", err.Error())
		return
	}
	status := http.StatusCreated
	if request.Method == http.MethodPut {
		status = http.StatusOK
	}
	writeJSON(w, status, item.Public())
}

func (r *Router) listAuditLogs(w http.ResponseWriter, request *http.Request) {
	limit := 100
	if value := request.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, "LIMIT_INVALID", "limit must be between 1 and 500")
			return
		}
		limit = parsed
	}
	query := audit.Query{Limit: limit, UpstreamID: request.URL.Query().Get("upstream_id"), Status: request.URL.Query().Get("status"), Search: request.URL.Query().Get("search")}
	writeJSON(w, http.StatusOK, map[string]any{"logs": r.audits.List(query)})
}

func (r *Router) clearAuditLogs(w http.ResponseWriter, _ *http.Request) {
	if err := r.audits.Clear(); err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT_CLEAR_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Router) auditStats(w http.ResponseWriter, request *http.Request) {
	days := 7
	if value := request.URL.Query().Get("days"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 365 {
			writeError(w, http.StatusBadRequest, "DAYS_INVALID", "days must be between 1 and 365")
			return
		}
		days = parsed
	}
	writeJSON(w, http.StatusOK, r.audits.Stats(days))
}

func (r *Router) getSettings(w http.ResponseWriter, _ *http.Request) {
	settings := r.audits.Settings()
	writeJSON(w, http.StatusOK, map[string]any{"audit": settings, "models": map[string]any{"hf_base_url": settings.HFBaseURL}})
}

type settingsRequest struct {
	Audit  audit.Settings `json:"audit"`
	Models *struct {
		HFBaseURL string `json:"hf_base_url"`
	} `json:"models"`
}

func (r *Router) putSettings(w http.ResponseWriter, request *http.Request) {
	var input settingsRequest
	if !decodeJSON(w, request, &input) {
		return
	}
	if input.Models != nil {
		input.Audit.HFBaseURL = strings.TrimSpace(input.Models.HFBaseURL)
	}
	if err := r.audits.Configure(input.Audit); err != nil {
		writeError(w, http.StatusBadRequest, "SETTINGS_INVALID", err.Error())
		return
	}
	r.getSettings(w, request)
}

type modelDownloadRequest struct {
	Repo     string `json:"repo"`
	Revision string `json:"revision,omitempty"`
	Variant  string `json:"variant,omitempty"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
}

func (r *Router) downloadModel(w http.ResponseWriter, request *http.Request) {
	var input modelDownloadRequest
	if !decodeJSON(w, request, &input) {
		return
	}
	if strings.TrimSpace(input.Repo) == "" {
		writeError(w, http.StatusBadRequest, "MODEL_REPO_REQUIRED", "repo is required")
		return
	}
	if input.Revision == "" {
		input.Revision = "main"
	}
	if input.Variant == "" {
		input.Variant = "q4f16"
	}
	if input.ID == "" {
		repoID := input.Repo
		if parsed, err := url.Parse(input.Repo); err == nil && parsed.IsAbs() {
			repoID = parsed.Path
		}
		repoID = strings.Trim(repoID, "/")
		input.ID = strings.NewReplacer("/", "-", "\\", "-", " ", "-", ".", "-").Replace(repoID) + "-" + input.Variant
	}
	if input.Name == "" {
		input.Name = input.ID
	}
	settings := r.audits.Settings()
	if input.BaseURL == "" {
		input.BaseURL = settings.HFBaseURL
	}
	if input.BaseURL == "" {
		input.BaseURL = "https://huggingface.co"
	}
	op, ctx := r.operations.Create("model.download")
	go func() {
		_ = r.operations.Update(op.ID, func(item *operation.Operation) {
			item.Status = operation.StatusRunning
			item.Progress = 5
			item.Message = "downloading model"
		})
		directory, err := modeldownload.Download(ctx, modeldownload.Config{Root: r.models.Root(), ID: input.ID, Name: input.Name, Repo: input.Repo, Revision: input.Revision, Variant: input.Variant, BaseURL: input.BaseURL})
		if err != nil {
			_ = r.operations.Fail(op.ID, err)
			return
		}
		if _, err = r.models.Scan(ctx); err != nil {
			_ = r.operations.Fail(op.ID, err)
			return
		}
		_ = r.operations.Complete(op.ID, map[string]any{"model_id": input.ID, "directory": directory})
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": op.ID, "model_id": input.ID})
}

func (r *Router) deleteUpstream(w http.ResponseWriter, request *http.Request) {
	if err := r.upstreams.Delete(request.PathValue("upstream_id")); err != nil {
		writeError(w, http.StatusNotFound, "UPSTREAM_NOT_FOUND", "upstream is not configured")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Router) listModels(w http.ResponseWriter, request *http.Request) {
	models, err := r.models.Scan(request.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MODEL_SCAN_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "runtime": r.models.RuntimeStatus()})
}

func (r *Router) modelCatalog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"models": modeldownload.Catalog()})
}

func (r *Router) scanModels(w http.ResponseWriter, request *http.Request) {
	models, err := r.models.Scan(request.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MODEL_SCAN_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "runtime": r.models.RuntimeStatus()})
}

func (r *Router) getModel(w http.ResponseWriter, request *http.Request) {
	item, err := r.models.Get(request.PathValue("model_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "MODEL_NOT_FOUND", "model package is not configured")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (r *Router) activeModel(w http.ResponseWriter, _ *http.Request) {
	metadata, active := r.models.Active()
	writeJSON(w, http.StatusOK, map[string]any{"active": active, "model": metadata, "runtime": r.models.RuntimeStatus()})
}

func (r *Router) activateModel(w http.ResponseWriter, request *http.Request) {
	op, err := r.models.Activate(request.PathValue("model_id"))
	if err != nil {
		status, code := http.StatusUnprocessableEntity, "MODEL_ACTIVATE_FAILED"
		if errors.Is(err, model.ErrNotFound) {
			status, code = http.StatusNotFound, "MODEL_NOT_FOUND"
		}
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"operation_id": op.ID, "status": op.Status})
}

func (r *Router) unloadModel(w http.ResponseWriter, request *http.Request) {
	if active, ok := r.models.Active(); ok && active.ID != request.PathValue("model_id") {
		writeError(w, http.StatusConflict, "MODEL_NOT_ACTIVE", "requested model is not active")
		return
	}
	if err := r.models.Unload(); err != nil {
		writeError(w, http.StatusInternalServerError, "MODEL_UNLOAD_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Router) listOperations(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"operations": r.operations.List()})
}

func (r *Router) getOperation(w http.ResponseWriter, request *http.Request) {
	op, err := r.operations.Get(request.PathValue("operation_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "OPERATION_NOT_FOUND", "operation is not configured")
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (r *Router) cancelOperation(w http.ResponseWriter, request *http.Request) {
	if err := r.operations.Cancel(request.PathValue("operation_id")); err != nil {
		writeError(w, http.StatusNotFound, "OPERATION_NOT_FOUND", "operation is not configured")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeJSON(w http.ResponseWriter, request *http.Request, destination any) bool {
	request.Body = http.MaxBytesReader(w, request.Body, maxAPIBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body must contain one JSON value")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func requestMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		if origin := r.Header.Get("Origin"); origin != "" {
			if !allowedOrigin(origin) {
				writeError(w, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "browser origin is not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Remask-Policy, X-Remask-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
		logger.Printf("http_request method=%s path=%s duration_ms=%d", r.Method, safePath(r.URL.Path), time.Since(started).Milliseconds())
	})
}

func allowedOrigin(origin string) bool {
	if origin == "tauri://localhost" || origin == "http://tauri.localhost" || origin == "https://tauri.localhost" {
		return true
	}
	return strings.HasPrefix(origin, "http://127.0.0.1:") || strings.HasPrefix(origin, "http://localhost:")
}

func safePath(path string) string {
	if strings.HasPrefix(path, "/proxy/") {
		parts := strings.SplitN(strings.TrimPrefix(path, "/proxy/"), "/", 2)
		if len(parts) > 0 {
			return "/proxy/" + parts[0] + "/..."
		}
	}
	return path
}
