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
	"github.com/remask/remask-core/internal/buildinfo"
	"github.com/remask/remask-core/internal/gateway"
	"github.com/remask/remask-core/internal/license"
	"github.com/remask/remask-core/internal/mitm"
	"github.com/remask/remask-core/internal/model"
	"github.com/remask/remask-core/internal/modeldownload"
	"github.com/remask/remask-core/internal/operation"
	"github.com/remask/remask-core/internal/pii"
	"github.com/remask/remask-core/internal/profile"
	"github.com/remask/remask-core/internal/proxyrule"
	"github.com/remask/remask-core/internal/scope"
	"github.com/remask/remask-core/internal/upstream"
)

const maxAPIBodyBytes = 2 << 20

type Router struct {
	logger     *log.Logger
	pii        *pii.Service
	profiles   *profile.Registry
	upstreams  *upstream.Registry
	proxyRules *proxyrule.Registry
	models     *model.Manager
	operations *operation.Store
	audits     *audit.Store
	rules      *pii.RuleDetector
	authority  *mitm.Authority
	licenses   *license.Manager
	startedAt  time.Time
}

func NewRouter(logger *log.Logger, service *pii.Service, profiles *profile.Registry, upstreams *upstream.Registry, proxyRules *proxyrule.Registry, models *model.Manager, operations *operation.Store, audits *audit.Store, authority *mitm.Authority, licenses *license.Manager, configuredRules ...*pii.RuleDetector) http.Handler {
	rules := pii.NewRuleDetector()
	if len(configuredRules) > 0 && configuredRules[0] != nil {
		rules = configuredRules[0]
	}
	router := &Router{
		logger: logger, pii: service, profiles: profiles, upstreams: upstreams, proxyRules: proxyRules, models: models, operations: operations, audits: audits, rules: rules, authority: authority, licenses: licenses,
		startedAt: time.Now().UTC(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", router.health)
	mux.HandleFunc("GET /api/v1/ready", router.ready)
	mux.HandleFunc("GET /api/v1/version", router.version)
	mux.HandleFunc("GET /api/v1/license", router.getLicense)
	mux.HandleFunc("POST /api/v1/license/import", router.importLicense)
	mux.HandleFunc("POST /api/v1/detect", router.detect)
	mux.HandleFunc("POST /api/v1/redact", router.redact)
	mux.HandleFunc("POST /api/v1/restore", router.restore)
	mux.HandleFunc("GET /api/v1/scopes/{scope_id}", router.getScope)
	mux.HandleFunc("DELETE /api/v1/scopes/{scope_id}", router.deleteScope)
	mux.HandleFunc("GET /api/v1/profiles", router.listProfiles)
	mux.HandleFunc("GET /api/v1/proxy/ca", router.proxyCAStatus)
	mux.HandleFunc("GET /api/v1/upstreams", router.listUpstreams)
	mux.HandleFunc("POST /api/v1/upstreams", router.putUpstream)
	mux.HandleFunc("GET /api/v1/upstreams/{upstream_id}", router.getUpstream)
	mux.HandleFunc("PUT /api/v1/upstreams/{upstream_id}", router.putUpstream)
	mux.HandleFunc("DELETE /api/v1/upstreams/{upstream_id}", router.deleteUpstream)
	mux.HandleFunc("GET /api/v1/proxy-rules", router.listProxyRules)
	mux.HandleFunc("POST /api/v1/proxy-rules", router.putProxyRule)
	mux.HandleFunc("PUT /api/v1/proxy-rules/{proxy_rule_id}", router.putProxyRule)
	mux.HandleFunc("DELETE /api/v1/proxy-rules/{proxy_rule_id}", router.deleteProxyRule)
	mux.HandleFunc("GET /api/v1/models", router.listModels)
	mux.HandleFunc("POST /api/v1/models/scan", router.scanModels)
	mux.HandleFunc("GET /api/v1/models/catalog", router.modelCatalog)
	mux.HandleFunc("POST /api/v1/models/download", router.downloadModel)
	mux.HandleFunc("GET /api/v1/models/active", router.activeModel)
	mux.HandleFunc("GET /api/v1/models/{model_id}", router.getModel)
	mux.HandleFunc("POST /api/v1/models/{model_id}/activate", router.activateModel)
	mux.HandleFunc("POST /api/v1/models/{model_id}/unload", router.unloadModel)
	mux.HandleFunc("DELETE /api/v1/models/{model_id}", router.deleteModel)
	mux.HandleFunc("GET /api/v1/operations", router.listOperations)
	mux.HandleFunc("GET /api/v1/operations/{operation_id}", router.getOperation)
	mux.HandleFunc("DELETE /api/v1/operations/{operation_id}", router.cancelOperation)
	mux.HandleFunc("GET /api/v1/audit/logs", router.listAuditLogs)
	mux.HandleFunc("GET /api/v1/audit/logs/{log_id}", router.getAuditLog)
	mux.HandleFunc("DELETE /api/v1/audit/logs", router.clearAuditLogs)
	mux.HandleFunc("GET /api/v1/audit/stats", router.auditStats)
	mux.HandleFunc("GET /api/v1/settings", router.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", router.putSettings)
	mux.HandleFunc("GET /api/v1/policy", router.getPolicy)
	mux.HandleFunc("PUT /api/v1/policy", router.putPolicy)
	return requestMiddleware(logger, mux)
}

func (r *Router) getLicense(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, r.licenses.State())
}

type licenseImportRequest struct {
	Content string `json:"content"`
}

func (r *Router) importLicense(w http.ResponseWriter, request *http.Request) {
	var input licenseImportRequest
	if !decodeJSON(w, request, &input) {
		return
	}
	state, err := r.licenses.Import([]byte(input.Content))
	if err != nil {
		writeError(w, http.StatusBadRequest, license.ErrorCode(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (r *Router) getPolicy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, r.rules.EffectivePolicy())
}

type policyRequest struct {
	Enabled              *bool                   `json:"enabled"`
	RedactAIAnswers      *bool                   `json:"redact_ai_answers"`
	RedactSystemMessages *bool                   `json:"redact_system_messages"`
	EntityTypes          *[]pii.EntityTypeConfig `json:"entity_types"`
	Rules                *[]pii.RuleConfig       `json:"rules"`
}

func (r *Router) putPolicy(w http.ResponseWriter, request *http.Request) {
	var input policyRequest
	if !decodeJSON(w, request, &input) {
		return
	}
	policy := r.rules.Policy()
	if input.Enabled != nil {
		policy.Enabled = *input.Enabled
	}
	if input.RedactAIAnswers != nil {
		policy.RedactAIAnswers = *input.RedactAIAnswers
	}
	if input.RedactSystemMessages != nil {
		policy.RedactSystemMessages = *input.RedactSystemMessages
	}
	if input.EntityTypes != nil {
		policy.EntityTypes = *input.EntityTypes
	}
	if input.Rules != nil {
		policy.Rules = *input.Rules
	}
	if input.Rules != nil && len(policy.Rules) > license.FreeRuleLimit && r.licenses.EnforceFeatures() && !r.licenses.Advanced() {
		writeError(w, http.StatusForbidden, "LICENSE_FEATURE_REQUIRED", "free edition supports at most 1 rule")
		return
	}
	if err := r.rules.Configure(policy); err != nil {
		writeError(w, http.StatusBadRequest, "POLICY_INVALID", err.Error())
		return
	}
	// Entity policy changes alter detector output, so cached results must not
	// survive the update.
	r.pii.ClearEntityCache()
	writeJSON(w, http.StatusOK, r.rules.EffectivePolicy())
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

func (r *Router) proxyCAStatus(w http.ResponseWriter, _ *http.Request) {
	if r.authority == nil {
		writeError(w, http.StatusServiceUnavailable, "PROXY_CA_UNAVAILABLE", "local proxy certificate authority is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, r.authority.Status())
}

func (r *Router) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": "remask-core", "version": buildinfo.Version, "api_version": buildinfo.APIVersion,
		"build_id": buildinfo.BuildID, "build_time": buildinfo.BuildTime,
		"capabilities":  []string{"license.offline", "license.device-bound", "pii.rules", "pii.rules.configurable", "pii.entity-toggle", "pii.entity-cache", "pii.redact", "pii.restore", "proxy.http-json", "proxy.sse", "proxy.service-id-route", "proxy.domain-route", "proxy.auto-route", "proxy.path-passthrough", "proxy.forward-http", "proxy.forward-connect", "proxy.socks5", "proxy.selective-mitm", "proxy.rules.persisted", "proxy.global-toggle", "models.manifest", "models.hot-swap", "audit.sqlite", "audit.masked-log", "audit.token-usage", "audit.stats", "settings.persisted", "upstreams.persisted"},
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
	if !r.licenses.Advanced() {
		writeJSON(w, http.StatusOK, map[string]any{"profiles": r.profiles.BuiltinsList()})
		return
	}
	if err := r.profiles.Refresh(); err != nil {
		writeError(w, http.StatusInternalServerError, "PROFILE_SCAN_FAILED", err.Error())
		return
	}
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

func (r *Router) listProxyRules(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"proxy_rules": r.proxyRules.List()})
}

func (r *Router) putProxyRule(w http.ResponseWriter, request *http.Request) {
	var item proxyrule.Rule
	if !decodeJSON(w, request, &item) {
		return
	}
	pathID := request.PathValue("proxy_rule_id")
	if pathID != "" {
		if item.ID != "" && item.ID != pathID {
			writeError(w, http.StatusBadRequest, "PROXY_RULE_ID_MISMATCH", "body id must match path id")
			return
		}
		item.ID = pathID
	}
	if _, ok := r.profiles.Get(item.ProfileID); !ok {
		writeError(w, http.StatusBadRequest, "PROFILE_NOT_FOUND", "profile_id is not configured")
		return
	}
	if err := r.proxyRules.Put(item); err != nil {
		writeError(w, http.StatusBadRequest, "PROXY_RULE_INVALID", err.Error())
		return
	}
	status := http.StatusCreated
	if request.Method == http.MethodPut {
		status = http.StatusOK
	}
	writeJSON(w, status, item)
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
	offset := 0
	if value := request.URL.Query().Get("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "OFFSET_INVALID", "offset must be a non-negative integer")
			return
		}
		offset = parsed
	}
	query := audit.Query{Limit: limit + 1, Offset: offset, UpstreamID: request.URL.Query().Get("upstream_id"), Status: request.URL.Query().Get("status"), Search: request.URL.Query().Get("search")}
	logs, err := r.audits.ListSummaries(query)
	if err != nil {
		if r.logger != nil {
			r.logger.Printf("audit_list_failed error=%v", err)
		}
		writeError(w, http.StatusInternalServerError, "AUDIT_LIST_FAILED", "failed to load request logs")
		return
	}
	hasMore := len(logs) > limit
	if hasMore {
		logs = logs[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "has_more": hasMore, "next_offset": offset + len(logs)})
}

func (r *Router) getAuditLog(w http.ResponseWriter, request *http.Request) {
	item, ok := r.audits.Get(request.PathValue("log_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "AUDIT_LOG_NOT_FOUND", "request log not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": item})
}

func (r *Router) clearAuditLogs(w http.ResponseWriter, _ *http.Request) {
	if err := r.audits.Clear(); err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT_CLEAR_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Router) auditStats(w http.ResponseWriter, request *http.Request) {
	if period := request.URL.Query().Get("range"); period != "" {
		switch period {
		case "today", "yesterday", "7d", "30d":
			writeJSON(w, http.StatusOK, r.audits.StatsRange(period))
		default:
			writeError(w, http.StatusBadRequest, "RANGE_INVALID", "range must be today, yesterday, 7d, or 30d")
		}
		return
	}
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
	entityCacheEnabledSet    bool
	entityCacheTTLSecondsSet bool
	recordRawRequestSet      bool
}

// UnmarshalJSON tracks the presence of optional settings so partial updates
// preserve values that were not included in the request.
func (r *settingsRequest) UnmarshalJSON(data []byte) error {
	type wire struct {
		Audit  json.RawMessage `json:"audit"`
		Models *struct {
			HFBaseURL string `json:"hf_base_url"`
		} `json:"models"`
	}
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if len(value.Audit) > 0 && string(value.Audit) != "null" {
		if err := json.Unmarshal(value.Audit, &r.Audit); err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(value.Audit, &fields); err != nil {
			return err
		}
		_, r.entityCacheEnabledSet = fields["entity_cache_enabled"]
		_, r.entityCacheTTLSecondsSet = fields["entity_cache_ttl_seconds"]
		_, r.recordRawRequestSet = fields["record_raw_request"]
	}
	r.Models = value.Models
	return nil
}

func (r *Router) putSettings(w http.ResponseWriter, request *http.Request) {
	var input settingsRequest
	if !decodeJSON(w, request, &input) {
		return
	}
	current := r.audits.Settings()
	if !input.entityCacheEnabledSet {
		input.Audit.EntityCacheEnabled = current.EntityCacheEnabled
	}
	if !input.entityCacheTTLSecondsSet {
		input.Audit.EntityCacheTTLSeconds = current.EntityCacheTTLSeconds
	}
	if !input.recordRawRequestSet {
		input.Audit.RecordRawRequest = current.RecordRawRequest
	}
	if input.Audit.RecordRawRequest && r.licenses.EnforceFeatures() && !r.licenses.Advanced() {
		writeError(w, http.StatusForbidden, "LICENSE_FEATURE_REQUIRED", "recording raw requests requires an advanced license")
		return
	}
	if input.Models != nil {
		input.Audit.HFBaseURL = strings.TrimSpace(input.Models.HFBaseURL)
	}
	if err := r.audits.Configure(input.Audit); err != nil {
		writeError(w, http.StatusBadRequest, "SETTINGS_INVALID", err.Error())
		return
	}
	if err := r.pii.ConfigureEntityCache(pii.EntityCacheConfig{
		Enabled: input.Audit.EntityCacheEnabled,
		TTL:     time.Duration(input.Audit.EntityCacheTTLSeconds) * time.Second,
	}); err != nil {
		writeError(w, http.StatusBadRequest, "SETTINGS_INVALID", err.Error())
		return
	}
	if err := r.models.SetProvider(input.Audit.InferenceProvider); err != nil {
		writeError(w, http.StatusBadRequest, "SETTINGS_INVALID", err.Error())
		return
	}
	r.models.SetMaxInferenceTokens(input.Audit.MaxInferenceTokens)
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
	if r.licenses.EnforceFeatures() && !r.licenses.Advanced() {
		writeError(w, http.StatusForbidden, "LICENSE_FEATURE_REQUIRED", "downloading or switching models requires an advanced license")
		return
	}
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
	input.Variant = strings.TrimSpace(input.Variant)
	if input.ID == "" {
		repoID := input.Repo
		if parsed, err := url.Parse(input.Repo); err == nil && parsed.IsAbs() {
			repoID = parsed.Path
		}
		repoID = strings.Trim(repoID, "/")
		input.ID = strings.NewReplacer("/", "-", "\\", "-", " ", "-", ".", "-").Replace(repoID)
		if input.Variant != "" {
			input.ID += "-" + input.Variant
		}
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
	// Resolve and validate the repository before enqueueing the download, so a
	// successful response means the model information was parsed and the model
	// can be shown in the list immediately.
	if err := modeldownload.ValidateRepo(request.Context(), modeldownload.Config{Root: r.models.Root(), ID: input.ID, Repo: input.Repo, Revision: input.Revision, Variant: input.Variant, BaseURL: input.BaseURL}); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "MODEL_REPO_INVALID", err.Error())
		return
	}
	op, ctx := r.operations.Create("model.download")
	go func() {
		progress := func(item *operation.Operation) {
			item.Status = operation.StatusRunning
			item.Message = "downloading model"
		}
		_ = r.operations.Update(op.ID, func(item *operation.Operation) {
			progress(item)
			item.Progress = 5
		})
		directory, err := modeldownload.Download(ctx, modeldownload.Config{Root: r.models.Root(), ID: input.ID, Name: input.Name, Repo: input.Repo, Revision: input.Revision, Variant: input.Variant, BaseURL: input.BaseURL})
		if err != nil {
			_ = r.operations.Fail(op.ID, err)
			return
		}
		_ = r.operations.Update(op.ID, func(item *operation.Operation) {
			progress(item)
			item.Progress = 85
			item.Message = "validating model"
		})
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

func (r *Router) deleteProxyRule(w http.ResponseWriter, request *http.Request) {
	if err := r.proxyRules.Delete(request.PathValue("proxy_rule_id")); err != nil {
		writeError(w, http.StatusNotFound, "PROXY_RULE_NOT_FOUND", "proxy rule is not configured")
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
	modelID := request.PathValue("model_id")
	item, err := r.models.Get(modelID)
	if err != nil {
		writeError(w, http.StatusNotFound, "MODEL_NOT_FOUND", "model package is not configured")
		return
	}
	if r.licenses.EnforceFeatures() && !r.licenses.Advanced() && !item.BuiltIn {
		writeError(w, http.StatusForbidden, "LICENSE_FEATURE_REQUIRED", "activating custom models requires an advanced license")
		return
	}
	// A model swap changes detection output. Clear before the asynchronous load
	// starts so no result from the previous model can leak into the new one.
	r.pii.ClearEntityCache()
	op, err := r.models.Activate(modelID)
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
	r.pii.ClearEntityCache()
	w.WriteHeader(http.StatusNoContent)
}

func (r *Router) deleteModel(w http.ResponseWriter, request *http.Request) {
	if err := r.models.Delete(request.PathValue("model_id")); err != nil {
		status, code := http.StatusConflict, "MODEL_DELETE_FAILED"
		if errors.Is(err, model.ErrNotFound) {
			status, code = http.StatusNotFound, "MODEL_NOT_FOUND"
		} else if errors.Is(err, model.ErrReadOnly) {
			status, code = http.StatusForbidden, "MODEL_READ_ONLY"
		}
		writeError(w, status, code, err.Error())
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
		defer func() {
			logger.Printf("http_request method=%s path=%s duration_ms=%d", r.Method, safePath(r.URL.Path), time.Since(started).Milliseconds())
		}()
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
