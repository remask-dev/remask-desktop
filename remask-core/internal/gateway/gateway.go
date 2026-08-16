package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/remask/remask-core/internal/audit"
	"github.com/remask/remask-core/internal/document"
	"github.com/remask/remask-core/internal/pii"
	"github.com/remask/remask-core/internal/profile"
	streamrestore "github.com/remask/remask-core/internal/stream"
	"github.com/remask/remask-core/internal/stream/sse"
	"github.com/remask/remask-core/internal/upstream"
)

const maxBodyBytes = 8 << 20

var errBodyTooLarge = errors.New("body exceeds configured limit")

type Gateway struct {
	logger     *log.Logger
	upstreams  *upstream.Registry
	profiles   *profile.Registry
	pii        *pii.Service
	documents  *document.Transformer
	httpClient *http.Client
	audits     *audit.Store
	rules      *pii.RuleDetector
}

type targetConfig struct {
	ID        string
	BaseURL   string
	ProfileID string
	APIKey    string
}

func targetFromUpstream(item upstream.Upstream) targetConfig {
	return targetConfig{ID: item.ID, BaseURL: item.BaseURL, ProfileID: item.ProfileID, APIKey: item.APIKey}
}

func New(logger *log.Logger, upstreams *upstream.Registry, profiles *profile.Registry, service *pii.Service, audits *audit.Store, httpClient *http.Client, configuredRules ...*pii.RuleDetector) *Gateway {
	if httpClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// The gateway is itself commonly launched with HTTPS_PROXY pointing at
		// Remask. Its upstream transport must never recurse through that proxy.
		transport.Proxy = nil
		httpClient = &http.Client{Transport: transport, Timeout: 5 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	rules := pii.NewRuleDetector()
	if len(configuredRules) > 0 && configuredRules[0] != nil {
		rules = configuredRules[0]
	}
	return &Gateway{
		logger: logger, upstreams: upstreams, profiles: profiles, pii: service,
		documents:  document.NewTransformer(),
		httpClient: httpClient,
		audits:     audits,
		rules:      rules,
	}
}

// DebugMode reports the live request tracing setting. It is intentionally
// read for every request so changing the setting takes effect immediately.
func (g *Gateway) DebugMode() bool {
	return g.audits != nil && g.audits.Settings().Debug
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	configured, upstreamPath, operation, matched, routeErr := g.resolveRoute(r.Method, r.URL.Path)
	if routeErr != nil {
		// An automatic route has no provider path to match against. For a
		// model-shaped POST, use the first configured service and let the
		// provider-agnostic operation protect the request body.
		if routeErr.code == "AUTO_UPSTREAM_NOT_FOUND" {
			if fallback, ok := g.resolveGenericRoute(r); ok {
				g.serveResolved(w, r, targetFromUpstream(fallback), r.URL.Path, profile.GenericOperation(), true)
				return
			}
		}
		writeProxyError(w, routeErr.status, routeErr.code, routeErr.message)
		return
	}
	g.serveResolved(w, r, targetFromUpstream(configured), upstreamPath, operation, matched)
}

// ServeProxyHTTP protects an explicit-proxy request without using a Provider
// as its destination. targetBaseURL is derived from the original proxy request;
// the rule contributes only its audit identity and protocol profile.
func (g *Gateway) ServeProxyHTTP(w http.ResponseWriter, r *http.Request, ruleID, profileID, targetBaseURL string) {
	g.serveResolved(w, r, targetConfig{ID: "proxy-rule:" + ruleID, BaseURL: targetBaseURL, ProfileID: profileID}, r.URL.Path, profile.Operation{}, false)
}

func (g *Gateway) serveResolved(w http.ResponseWriter, r *http.Request, configured targetConfig, upstreamPath string, operation profile.Operation, matched bool) {
	redactionDuration := int64(0)
	debug := g.DebugMode()
	counted := &countingResponseWriter{ResponseWriter: w}
	var requestBody bytes.Buffer
	var requestHeaders http.Header
	if debug {
		counted.capture = &bytes.Buffer{}
		requestHeaders = r.Header.Clone()
	}
	w = counted
	operationErr := profile.ErrNoMatch
	if matched {
		operationErr = nil
	} else {
		operation, operationErr = g.profiles.Match(configured.ProfileID, r.Method, upstreamPath)
	}
	passthrough := operationErr != nil || operation.Passthrough || !g.rules.Enabled()
	operationID := operation.ID
	protectionMode := "redacted"
	if passthrough {
		protectionMode = "passthrough"
		if operationErr != nil {
			operationID = "passthrough"
		} else if !operation.Passthrough && !g.rules.Enabled() {
			operationID, protectionMode = "disabled", "disabled"
		}
	}
	auditEntry := audit.Entry{
		Timestamp: time.Now().UTC(), UpstreamID: configured.ID, ProfileID: configured.ProfileID,
		OperationID: operationID, Model: extractModelFromPath(upstreamPath), ProtectionMode: protectionMode,
		Method: r.Method, Path: upstreamPath,
	}
	defer func() {
		for _, field := range auditEntry.Fields {
			auditEntry.EntityCount += len(field.Entities)
		}
		auditEntry.StatusCode = counted.StatusCode()
		auditEntry.DurationMS = redactionDuration
		auditEntry.ResponseBytes = counted.bytes
		if debug {
			auditEntry.Debug = &audit.DebugExchange{
				Request: audit.DebugRequest{
					Method: r.Method, URL: r.URL.RequestURI(),
					Headers: map[string][]string(requestHeaders), Body: requestBody.String(),
				},
				Response: audit.DebugResponse{
					Status: counted.StatusCode(), Headers: map[string][]string(counted.Header().Clone()),
					Body: counted.capture.String(),
				},
			}
		}
		if err := g.audits.Add(auditEntry); err != nil {
			g.logger.Printf("audit_write_failed upstream=%s error=%v", configured.ID, err)
		}
	}()

	wireBody, err := readBody(r.Body, maxBodyBytes)
	if debug {
		_, _ = requestBody.Write(wireBody)
	}
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			passthrough = true
			auditEntry.ProtectionMode = "passthrough"
			if r.ContentLength > 0 {
				auditEntry.RequestBytes = r.ContentLength
			} else {
				auditEntry.RequestBytes = int64(len(wireBody))
			}
			remaining := io.Reader(r.Body)
			if debug {
				remaining = io.TeeReader(remaining, &requestBody)
			}
			g.serveOversizedPassthrough(w, r, configured, upstreamPath, io.MultiReader(bytes.NewReader(wireBody), remaining), &auditEntry)
			return
		}
		writeProxyError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_INVALID", err.Error())
		return
	}
	auditEntry.RequestBytes = int64(len(wireBody))
	contentEncoding := strings.TrimSpace(strings.ToLower(r.Header.Get("Content-Encoding")))
	body := wireBody
	var decodingErr error
	if contentEncoding == "gzip" {
		body, decodingErr = decodeGzipBody(wireBody, maxBodyBytes)
		if decodingErr == nil && debug {
			requestBody.Reset()
			_, _ = requestBody.Write(body)
			requestHeaders.Del("Content-Encoding")
			requestHeaders.Del("Content-Length")
		}
	}
	if decodingErr == nil {
		if model := extractModelFromBody(body); model != "" {
			auditEntry.Model = model
		}
	}
	if decodingErr == nil && operationErr != nil && isJSON(r.Header.Get("Content-Type")) {
		if fallback, fallbackErr := profile.GenericMatch(r.Method, body); fallbackErr == nil {
			operation = fallback
			operationErr = nil
			operationID = operation.ID
			if g.rules.Enabled() {
				protectionMode = "redacted"
			} else {
				protectionMode = "disabled"
			}
			auditEntry.OperationID = operationID
			auditEntry.ProtectionMode = protectionMode
		}
	}
	passthrough = operationErr != nil || operation.Passthrough || !g.rules.Enabled()
	if !passthrough && decodingErr != nil {
		auditEntry.ErrorCode = "REQUEST_BODY_INVALID"
		if errors.Is(decodingErr, errBodyTooLarge) {
			writeProxyError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_INVALID", "decompressed body exceeds configured limit")
		} else {
			writeProxyError(w, http.StatusBadRequest, "REQUEST_BODY_INVALID", "invalid gzip request body")
		}
		return
	}
	if !passthrough && (len(body) == 0 || !isJSON(r.Header.Get("Content-Type")) || !json.Valid(body) || (contentEncoding != "" && contentEncoding != "identity" && contentEncoding != "gzip")) {
		// A matched route with a representation we do not understand must remain
		// available, but must never be reported as protected.
		passthrough = true
		protectionMode = "passthrough"
		auditEntry.ProtectionMode = protectionMode
	}

	forwardBody := body
	scopeID := ""
	if !passthrough && len(body) > 0 && isJSON(r.Header.Get("Content-Type")) {
		redactStarted := time.Now()
		forwardBody, scopeID, auditEntry.Fields, err = g.redactJSON(r.Context(), body, operation, g.rules.RedactAIAnswers(), scopeID)
		redactionDuration += time.Since(redactStarted).Milliseconds()
		if err != nil {
			auditEntry.ErrorCode = "REDACTION_FAILED"
			writeProxyError(w, http.StatusUnprocessableEntity, "REDACTION_FAILED", err.Error())
			return
		}
	}
	if passthrough {
		redactionDuration = 0
		forwardBody = wireBody
	} else if contentEncoding == "gzip" {
		forwardBody, err = encodeGzipBody(forwardBody)
		if err != nil {
			auditEntry.ErrorCode = "REQUEST_RECOMPRESSION_FAILED"
			writeProxyError(w, http.StatusInternalServerError, "REQUEST_RECOMPRESSION_FAILED", err.Error())
			return
		}
	}
	if scopeID != "" {
		defer g.pii.Store().Delete(context.Background(), scopeID)
	}

	target, err := buildTarget(configured.BaseURL, upstreamPath, r.URL.RawQuery)
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError, "UPSTREAM_URL_INVALID", err.Error())
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(forwardBody))
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError, "UPSTREAM_REQUEST_FAILED", err.Error())
		return
	}
	copyRequestHeaders(request.Header, r.Header)
	g.applyManagedHeaders(request, configured)
	request.Header.Del("X-Remask-Policy")
	request.Header.Del("X-Remask-Scope")
	request.Header.Del("X-Remask-Request-ID")
	request.Header.Set("Accept-Encoding", "identity")

	response, err := g.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		auditEntry.ErrorCode = "UPSTREAM_UNAVAILABLE"
		writeProxyError(w, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", err.Error())
		return
	}
	defer response.Body.Close()
	responseEncoding := strings.TrimSpace(strings.ToLower(response.Header.Get("Content-Encoding")))
	if responseEncoding != "" && responseEncoding != "identity" {
		auditEntry.Streaming = strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream")
		g.servePassthrough(w, response, auditEntry.Streaming, &auditEntry.TokenUsage)
		return
	}
	if passthrough {
		auditEntry.Streaming = strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream")
		g.servePassthrough(w, response, auditEntry.Streaming, &auditEntry.TokenUsage)
		return
	}

	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		auditEntry.Streaming = true
		g.serveSSE(w, r, response, operation, scopeID, &auditEntry.TokenUsage)
		return
	}

	responseBody, err := readBody(response.Body, maxBodyBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			copyResponseHeaders(w.Header(), response.Header)
			w.Header().Del("ETag")
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(responseBody)
			_, _ = io.Copy(w, response.Body)
			return
		}
		writeProxyError(w, http.StatusBadGateway, "UPSTREAM_RESPONSE_INVALID", err.Error())
		return
	}
	auditEntry.TokenUsage = extractTokenUsage(responseBody)
	if len(responseBody) > 0 && isJSON(response.Header.Get("Content-Type")) && scopeID != "" {
		responseBody, err = g.restoreJSON(r.Context(), responseBody, operation.ResponseTextFields, scopeID)
		if err != nil {
			writeProxyError(w, http.StatusBadGateway, "RESTORE_FAILED", err.Error())
			return
		}
	}

	copyResponseHeaders(w.Header(), response.Header)
	w.Header().Del("Content-Length")
	w.Header().Del("ETag")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func (g *Gateway) serveOversizedPassthrough(w http.ResponseWriter, r *http.Request, configured targetConfig, upstreamPath string, body io.Reader, auditEntry *audit.Entry) {
	target, err := buildTarget(configured.BaseURL, upstreamPath, r.URL.RawQuery)
	if err != nil {
		auditEntry.ErrorCode = "UPSTREAM_URL_INVALID"
		writeProxyError(w, http.StatusInternalServerError, "UPSTREAM_URL_INVALID", err.Error())
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
	if err != nil {
		auditEntry.ErrorCode = "UPSTREAM_REQUEST_FAILED"
		writeProxyError(w, http.StatusInternalServerError, "UPSTREAM_REQUEST_FAILED", err.Error())
		return
	}
	request.ContentLength = r.ContentLength
	copyRequestHeaders(request.Header, r.Header)
	g.applyManagedHeaders(request, configured)
	request.Header.Del("X-Remask-Policy")
	request.Header.Del("X-Remask-Scope")
	request.Header.Del("X-Remask-Request-ID")

	response, err := g.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		auditEntry.ErrorCode = "UPSTREAM_UNAVAILABLE"
		writeProxyError(w, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", err.Error())
		return
	}
	defer response.Body.Close()
	streaming := strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream")
	auditEntry.Streaming = streaming
	g.servePassthrough(w, response, streaming, &auditEntry.TokenUsage)
}

func (g *Gateway) applyManagedHeaders(request *http.Request, configured targetConfig) {
	if configured.APIKey == "" {
		return
	}
	if configuredProfile, ok := g.profiles.Get(configured.ProfileID); ok {
		for key, template := range configuredProfile.HeaderTemplates {
			request.Header.Set(key, strings.ReplaceAll(template, "{{api_key}}", configured.APIKey))
		}
	}
}

type routeError struct {
	status  int
	code    string
	message string
}

func (g *Gateway) resolveRoute(method, requestPath string) (upstream.Upstream, string, profile.Operation, bool, *routeError) {
	if strings.HasPrefix(requestPath, "/proxy/") {
		selector, upstreamPath, ok := splitProxyPath(requestPath)
		if !ok {
			return upstream.Upstream{}, "", profile.Operation{}, false, &routeError{
				status: http.StatusNotFound, code: "INVALID_PROXY_PATH", message: "expected /proxy/{service-id-or-domain}/{path}",
			}
		}
		configured, err := g.upstreams.Get(selector)
		if err == nil {
			if !configured.Enabled {
				return upstream.Upstream{}, "", profile.Operation{}, false, &routeError{
					status: http.StatusServiceUnavailable, code: "UPSTREAM_DISABLED", message: "the selected provider is disabled",
				}
			}
			return configured, upstreamPath, profile.Operation{}, false, nil
		}
		matches := g.upstreamsByDomain(selector)
		if len(matches) > 0 {
			return matches[0], upstreamPath, profile.Operation{}, false, nil
		}
		return upstream.Upstream{}, "", profile.Operation{}, false, &routeError{
			status: http.StatusNotFound, code: "UPSTREAM_NOT_FOUND", message: "service ID or configured upstream domain was not found",
		}
	}

	type candidate struct {
		upstream  upstream.Upstream
		operation profile.Operation
	}
	candidates := make([]candidate, 0, 2)
	for _, configured := range g.upstreams.List() {
		if !configured.Enabled {
			continue
		}
		operation, err := g.profiles.Match(configured.ProfileID, method, requestPath)
		if err == nil {
			candidates = append(candidates, candidate{upstream: configured, operation: operation})
		}
	}
	if len(candidates) > 0 {
		return candidates[0].upstream, requestPath, candidates[0].operation, true, nil
	}
	return upstream.Upstream{}, "", profile.Operation{}, false, &routeError{
		status: http.StatusNotFound, code: "AUTO_UPSTREAM_NOT_FOUND", message: "no configured service profile matches this method and path",
	}
}

func (g *Gateway) resolveGenericRoute(r *http.Request) (upstream.Upstream, bool) {
	if !strings.EqualFold(r.Method, http.MethodPost) || !isJSON(r.Header.Get("Content-Type")) {
		return upstream.Upstream{}, false
	}
	wireBody, err := readBody(r.Body, maxBodyBytes)
	if err != nil {
		return upstream.Upstream{}, false
	}
	// The normal request path still owns the body; restore it after the route
	// probe so redaction and forwarding see the original bytes.
	r.Body = io.NopCloser(bytes.NewReader(wireBody))
	body := wireBody
	switch strings.TrimSpace(strings.ToLower(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		body, err = decodeGzipBody(wireBody, maxBodyBytes)
		if err != nil {
			return upstream.Upstream{}, false
		}
	default:
		return upstream.Upstream{}, false
	}
	if _, err := profile.GenericMatch(r.Method, body); err != nil {
		return upstream.Upstream{}, false
	}
	for _, configured := range g.upstreams.List() {
		if configured.Enabled {
			return configured, true
		}
	}
	return upstream.Upstream{}, false
}

func (g *Gateway) upstreamsByDomain(domain string) []upstream.Upstream {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	result := make([]upstream.Upstream, 0, 1)
	for _, configured := range g.upstreams.List() {
		if !configured.Enabled {
			continue
		}
		parsed, err := url.Parse(configured.BaseURL)
		if err != nil {
			continue
		}
		host := strings.TrimSuffix(strings.ToLower(parsed.Host), ".")
		hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
		if domain == host || domain == hostname {
			result = append(result, configured)
		}
	}
	return result
}

func (g *Gateway) servePassthrough(w http.ResponseWriter, response *http.Response, streaming bool, usage *audit.TokenUsage) {
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	capture := &limitedCapture{limit: maxBodyBytes}
	reader := io.TeeReader(response.Body, capture)
	if !streaming {
		_, _ = io.Copy(w, reader)
		*usage = extractTokenUsage(capture.Bytes())
		return
	}
	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			_, _ = w.Write(buffer[:read])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			*usage = extractTokenUsage(capture.Bytes())
			return
		}
	}
}

type streamTemplate struct {
	event sse.Event
	path  string
}

func (g *Gateway) serveSSE(w http.ResponseWriter, request *http.Request, response *http.Response, operation profile.Operation, scopeID string, usage *audit.TokenUsage) {
	if scopeID == "" {
		copyResponseHeaders(w.Header(), response.Header)
		w.Header().Del("Content-Length")
		w.WriteHeader(response.StatusCode)
		capture := &limitedCapture{limit: maxBodyBytes}
		_, _ = io.Copy(w, io.TeeReader(response.Body, capture))
		*usage = extractTokenUsage(capture.Bytes())
		return
	}
	vault, err := g.pii.Store().Get(request.Context(), scopeID)
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError, "SCOPE_NOT_FOUND", err.Error())
		return
	}
	restorer := streamrestore.NewRestorer(vault.Resolve)
	templates := make(map[string]streamTemplate)

	copyResponseHeaders(w.Header(), response.Header)
	w.Header().Del("Content-Length")
	w.Header().Del("ETag")
	w.WriteHeader(response.StatusCode)
	flusher, _ := w.(http.Flusher)
	decoder := sse.NewDecoder(response.Body)

	for {
		event, decodeErr := decoder.Decode()
		if errors.Is(decodeErr, io.EOF) {
			g.flushPendingSSE(w, restorer, templates)
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		if decodeErr != nil {
			g.logger.Printf("sse_decode_failed upstream_path=%s error=%v", request.URL.Path, decodeErr)
			g.flushPendingSSE(w, restorer, templates)
			return
		}

		if isTerminalEvent(event, operation) {
			g.flushPendingSSE(w, restorer, templates)
			_, _ = w.Write(sse.Encode(event))
			if flusher != nil {
				flusher.Flush()
			}
			continue
		}

		data := event.DataString()
		mergeTokenUsage(usage, extractTokenUsage([]byte(data)))
		if len(operation.StreamTextFields) > 0 && looksLikeJSON(data) {
			channelSuffix := streamChannelSuffix(g.documents, []byte(data), operation.StreamChannelFields)
			transformed, transformErr := g.documents.TransformJSONMatches([]byte(data), operation.StreamTextFields, func(match document.TextMatch) (string, error) {
				channel := event.Event + "|" + match.Path + channelSuffix
				templates[channel] = streamTemplate{event: event, path: match.Path}
				return restorer.Feed(channel, match.Value), nil
			})
			if transformErr != nil {
				g.logger.Printf("sse_transform_failed upstream_path=%s error=%v", request.URL.Path, transformErr)
			} else {
				event.SetData(string(transformed))
			}
		}
		_, _ = w.Write(sse.Encode(event))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (g *Gateway) flushPendingSSE(w http.ResponseWriter, restorer *streamrestore.Restorer, templates map[string]streamTemplate) {
	pending := restorer.FlushAll()
	channels := make([]string, 0, len(pending))
	for channel := range pending {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	for _, channel := range channels {
		text := pending[channel]
		if text == "" {
			continue
		}
		template, ok := templates[channel]
		if !ok {
			continue
		}
		data := template.event.DataString()
		transformed, err := g.documents.TransformJSONMatches([]byte(data), []string{template.path}, func(match document.TextMatch) (string, error) {
			return text, nil
		})
		if err != nil {
			continue
		}
		event := template.event
		event.SetData(string(transformed))
		_, _ = w.Write(sse.Encode(event))
	}
}

func isTerminalEvent(event sse.Event, operation profile.Operation) bool {
	for _, value := range operation.StreamTerminalData {
		if event.DataString() == value {
			return true
		}
	}
	for _, value := range operation.StreamTerminalEvents {
		if event.Event == value {
			return true
		}
	}
	return false
}

func streamChannelSuffix(transformer *document.Transformer, data []byte, selectors []string) string {
	matches, err := transformer.ExtractScalars(data, selectors)
	if err != nil || len(matches) == 0 {
		return ""
	}
	parts := make([]string, 0, len(matches))
	for _, match := range matches {
		parts = append(parts, match.Path+"="+match.Value)
	}
	sort.Strings(parts)
	return "|" + strings.Join(parts, "|")
}

func looksLikeJSON(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[")
}

func (g *Gateway) redactJSON(ctx context.Context, body []byte, operation profile.Operation, redactAIAnswers bool, scopeID string) ([]byte, string, []audit.Field, error) {
	currentScope := scopeID
	assistantPaths := make(map[string]struct{})
	if !redactAIAnswers {
		roles, err := g.documents.ExtractStrings(body, operation.AssistantRoleFields)
		if err != nil {
			return nil, scopeID, nil, err
		}
		for _, role := range roles {
			if stringInSliceFold(role.Value, operation.AssistantRoles) {
				if separator := strings.LastIndexByte(role.Path, '/'); separator > 0 {
					assistantPaths[role.Path[:separator]] = struct{}{}
				}
			}
		}
	}
	fields := make([]audit.Field, 0)
	transformed, err := g.documents.TransformJSONMatches(body, operation.RequestTextFields, func(match document.TextMatch) (string, error) {
		if hasPathAncestor(match.Path, assistantPaths) {
			return match.Value, nil
		}
		result, err := g.pii.Redact(ctx, match.Value, currentScope)
		if err != nil {
			return "", err
		}
		currentScope = result.ScopeID
		field := audit.Field{Path: match.Path, OriginalMasked: maskOriginal(match.Value, result.Entities), Redacted: result.Text}
		for _, entity := range result.Entities {
			masked := "***"
			if entity.StartByte >= 0 && entity.EndByte <= len(match.Value) && entity.StartByte < entity.EndByte {
				masked = maskValue(match.Value[entity.StartByte:entity.EndByte])
			}
			field.Entities = append(field.Entities, audit.Entity{
				Type: entity.Type, Replacement: entity.Replacement, Masked: masked, Confidence: entity.Confidence, Sources: entity.Sources,
			})
		}
		fields = append(fields, field)
		return result.Text, nil
	})
	return transformed, currentScope, fields, err
}

func hasPathAncestor(path string, ancestors map[string]struct{}) bool {
	for ancestor := range ancestors {
		if path == ancestor || strings.HasPrefix(path, ancestor+"/") {
			return true
		}
	}
	return false
}

func stringInSliceFold(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
}

func maskOriginal(text string, entities []pii.Entity) string {
	masked := text
	for index := len(entities) - 1; index >= 0; index-- {
		entity := entities[index]
		if entity.StartByte < 0 || entity.EndByte > len(masked) || entity.StartByte >= entity.EndByte {
			continue
		}
		masked = masked[:entity.StartByte] + maskValue(masked[entity.StartByte:entity.EndByte]) + masked[entity.EndByte:]
	}
	return masked
}

func maskValue(value string) string {
	runes := []rune(value)
	if len(runes) <= 2 {
		return strings.Repeat("*", len(runes))
	}
	keep := 3
	if len(runes) < 7 {
		keep = 1
	}
	if keep*2 >= len(runes) {
		keep = 1
	}
	return string(runes[:keep]) + "***" + string(runes[len(runes)-keep:])
}

type limitedCapture struct {
	buffer bytes.Buffer
	limit  int64
}

func (w *limitedCapture) Write(value []byte) (int, error) {
	original := len(value)
	remaining := w.limit - int64(w.buffer.Len())
	if remaining > 0 {
		if int64(len(value)) > remaining {
			value = value[:remaining]
		}
		_, _ = w.buffer.Write(value)
	}
	return original, nil
}
func (w *limitedCapture) Bytes() []byte { return w.buffer.Bytes() }

func extractTokenUsage(body []byte) audit.TokenUsage {
	var root any
	if json.Unmarshal(body, &root) != nil {
		result := audit.TokenUsage{}
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var value any
			if json.Unmarshal([]byte(line), &value) == nil {
				mergeTokenUsage(&result, usageFromValue(value))
			}
		}
		return result
	}
	return usageFromValue(root)
}

// extractModelFromBody reads only the top-level model field. Model names are
// request metadata and do not need the more permissive recursive traversal used
// for token usage extraction.
func extractModelFromBody(body []byte) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return ""
	}
	var model string
	if err := json.Unmarshal(object["model"], &model); err != nil {
		return ""
	}
	return strings.TrimSpace(model)
}

// Some providers, notably Gemini, encode the requested model in the route
// (for example /v1beta/models/gemini-2.0-flash:generateContent).
func extractModelFromPath(path string) string {
	const marker = "/models/"
	index := strings.LastIndex(path, marker)
	if index < 0 {
		return ""
	}
	model := path[index+len(marker):]
	if end := strings.IndexAny(model, "/:"); end >= 0 {
		model = model[:end]
	}
	return strings.TrimSpace(model)
}

func usageFromValue(value any) audit.TokenUsage {
	result := audit.TokenUsage{}
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "cached_tokens" || key == "cache_read_input_tokens" || key == "cachedContentTokenCount" || key == "prompt_cache_hit_tokens" {
					if number, ok := child.(float64); ok && int(number) > result.Cached {
						result.Cached = int(number)
					}
				}
				number, ok := child.(float64)
				if ok {
					n := int(number)
					switch key {
					case "prompt_tokens", "input_tokens", "prompt_token_count", "promptTokenCount":
						if n > result.Input {
							result.Input = n
						}
					case "completion_tokens", "output_tokens", "candidates_token_count", "candidatesTokenCount":
						if n > result.Output {
							result.Output = n
						}
					case "total_tokens", "total_token_count", "totalTokenCount":
						if n > result.Total {
							result.Total = n
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	if result.Total == 0 {
		result.Total = result.Input + result.Output
	}
	return result
}
func mergeTokenUsage(target *audit.TokenUsage, value audit.TokenUsage) {
	if value.Input > target.Input {
		target.Input = value.Input
	}
	if value.Output > target.Output {
		target.Output = value.Output
	}
	if value.Total > target.Total {
		target.Total = value.Total
	}
	if target.Total == 0 {
		target.Total = target.Input + target.Output
	}
	if value.Cached > target.Cached {
		target.Cached = value.Cached
	}
}

type countingResponseWriter struct {
	http.ResponseWriter
	status  int
	bytes   int64
	capture *bytes.Buffer
}

func (w *countingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *countingResponseWriter) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	written, err := w.ResponseWriter.Write(value)
	w.bytes += int64(written)
	if w.capture != nil && written > 0 {
		_, _ = w.capture.Write(value[:written])
	}
	return written, err
}

func (w *countingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *countingResponseWriter) StatusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (g *Gateway) restoreJSON(ctx context.Context, body []byte, selectors []string, scopeID string) ([]byte, error) {
	return g.documents.TransformJSON(body, selectors, func(text string) (string, error) {
		result, err := g.pii.Restore(ctx, scopeID, text)
		return result.Text, err
	})
}

func splitProxyPath(requestPath string) (string, string, bool) {
	trimmed := strings.TrimPrefix(requestPath, "/proxy/")
	if trimmed == requestPath {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", "", false
	}
	return parts[0], "/" + parts[1], true
}

func buildTarget(baseURL, requestPath, rawQuery string) (*url.URL, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + requestPath
	base.RawQuery = rawQuery
	return base, nil
}

func readBody(reader io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return body, errBodyTooLarge
	}
	return body, nil
}

func decodeGzipBody(body []byte, limit int64) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	decompressed, readErr := readBody(reader, limit)
	closeErr := reader.Close()
	if readErr != nil {
		return decompressed, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return decompressed, nil
}

func encodeGzipBody(body []byte) ([]byte, error) {
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	if _, err := writer.Write(body); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func isJSON(contentType string) bool {
	contentType = strings.ToLower(contentType)
	return strings.Contains(contentType, "application/json") || strings.Contains(contentType, "+json")
}

func copyRequestHeaders(destination, source http.Header) {
	for key, values := range source {
		if isHopByHop(key) || strings.EqualFold(key, "Host") || strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func copyResponseHeaders(destination, source http.Header) {
	for key, values := range source {
		if isHopByHop(key) || strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func isHopByHop(header string) bool {
	switch strings.ToLower(header) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func writeProxyError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"error":{"code":"`+code+`","message":"`+strings.ReplaceAll(message, `"`, `'`)+`"}}`)
}
