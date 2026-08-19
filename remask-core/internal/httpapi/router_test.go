package httpapi_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/remask/remask-core/internal/app"
	"github.com/remask/remask-core/internal/pii"
)

func TestRedactAndRestoreAPI(t *testing.T) {
	handler := testHandler(t)
	redact := httptest.NewRequest(http.MethodPost, "/api/v1/redact", strings.NewReader(`{"text":"密钥 sk-test-1234567890123456"}`))
	redact.Header.Set("Content-Type", "application/json")
	redactResponse := httptest.NewRecorder()
	handler.ServeHTTP(redactResponse, redact)
	if redactResponse.Code != http.StatusOK {
		t.Fatalf("redact status %d: %s", redactResponse.Code, redactResponse.Body.String())
	}
	var redacted struct {
		Text     string `json:"text"`
		ScopeID  string `json:"scope_id"`
		Entities []struct {
			Replacement string `json:"replacement"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(redactResponse.Body.Bytes(), &redacted); err != nil {
		t.Fatal(err)
	}
	if len(redacted.Entities) != 1 || !strings.Contains(redacted.Text, redacted.Entities[0].Replacement) {
		t.Fatalf("unexpected redact body: %s", redactResponse.Body.String())
	}

	restoreBody, _ := json.Marshal(map[string]string{"scope_id": redacted.ScopeID, "text": redacted.Text})
	restore := httptest.NewRequest(http.MethodPost, "/api/v1/restore", bytes.NewReader(restoreBody))
	restore.Header.Set("Content-Type", "application/json")
	restoreResponse := httptest.NewRecorder()
	handler.ServeHTTP(restoreResponse, restore)
	if restoreResponse.Code != http.StatusOK || !strings.Contains(restoreResponse.Body.String(), "sk-test-1234567890123456") {
		t.Fatalf("restore response: %d %s", restoreResponse.Code, restoreResponse.Body.String())
	}
}

func TestProxyCAStatusAPI(t *testing.T) {
	handler := testHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/proxy/ca", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":true`) || !strings.Contains(response.Body.String(), `"fingerprint_sha256"`) {
		t.Fatalf("proxy CA status: %d %s", response.Code, response.Body.String())
	}
}

func TestJSONProxyRedactsRequestAndRestoresResponse(t *testing.T) {
	var upstreamBody []byte
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamBody, _ = io.ReadAll(r.Body)
		body := `{"choices":[{"message":{"content":"收到，请联系 ` + extractToken(string(upstreamBody)) + `"}}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}

	handler := testHandlerWithClient(t, client)
	configure := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams", strings.NewReader(`{"id":"mock","base_url":"https://mock.example","profile_id":"openai","credential_mode":"passthrough","enabled":true}`))
	configure.Header.Set("Content-Type", "application/json")
	configureResponse := httptest.NewRecorder()
	handler.ServeHTTP(configureResponse, configure)
	if configureResponse.Code != http.StatusCreated {
		t.Fatalf("configure upstream: %d %s", configureResponse.Code, configureResponse.Body.String())
	}

	proxyRequest := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"密钥 sk-test-1234567890123456"}]}`))
	proxyRequest.Header.Set("Content-Type", "application/json")
	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(proxyResponse, proxyRequest)
	if proxyResponse.Code != http.StatusOK {
		t.Fatalf("proxy response: %d %s", proxyResponse.Code, proxyResponse.Body.String())
	}
	if strings.Contains(string(upstreamBody), "sk-test-1234567890123456") || !strings.Contains(string(upstreamBody), "<MASK_SECRET_KEY:") {
		t.Fatalf("upstream received unredacted content: %s", upstreamBody)
	}
	if !strings.Contains(proxyResponse.Body.String(), "sk-test-1234567890123456") {
		t.Fatalf("client response was not restored: %s", proxyResponse.Body.String())
	}

	logsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/audit/logs", nil)
	logsResponse := httptest.NewRecorder()
	handler.ServeHTTP(logsResponse, logsRequest)
	if logsResponse.Code != http.StatusOK {
		t.Fatalf("logs response: %d %s", logsResponse.Code, logsResponse.Body.String())
	}
	logsBody := logsResponse.Body.String()
	if strings.Contains(logsBody, "sk-test-1234567890123456") || strings.Contains(logsBody, "sk-***456") || strings.Contains(logsBody, `MASK_SECRET_KEY`) || strings.Contains(logsBody, `"fields"`) {
		t.Fatalf("audit list included request content: %s", logsBody)
	}
	if !strings.Contains(logsBody, `"gateway_type":"api_gateway"`) {
		t.Fatalf("audit list did not identify the API gateway: %s", logsBody)
	}
	if !strings.Contains(logsBody, `"target_host":"mock.example"`) {
		t.Fatalf("audit list did not include the target host: %s", logsBody)
	}
	var listed struct {
		Logs []struct {
			ID string `json:"id"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(logsResponse.Body.Bytes(), &listed); err != nil || len(listed.Logs) != 1 {
		t.Fatalf("decode audit list: %v body=%s", err, logsBody)
	}
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, httptest.NewRequest(http.MethodGet, "/api/v1/audit/logs/"+listed.Logs[0].ID, nil))
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("gzip audit detail: %d %s", detailResponse.Code, detailResponse.Body.String())
	}
	detailBody := detailResponse.Body.String()
	if detailResponse.Code != http.StatusOK || !strings.Contains(detailBody, "sk-test-1234567890123456") || !strings.Contains(detailBody, `\u003cMASK_SECRET_KEY:`) {
		t.Fatalf("audit detail did not include the original and redacted values: %d %s", detailResponse.Code, detailBody)
	}
}

func TestGzipJSONProxyRedactsAndRecompressesRequest(t *testing.T) {
	var upstreamBody []byte
	var upstreamEncoding string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamEncoding = r.Header.Get("Content-Encoding")
		compressed, _ := io.ReadAll(r.Body)
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatalf("open upstream gzip body: %v", err)
		}
		upstreamBody, err = io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read upstream gzip body: %v", err)
		}
		if err := reader.Close(); err != nil {
			t.Fatalf("close upstream gzip body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    r,
		}, nil
	})}

	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	settings := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"audit":{"record_request_content":true,"record_raw_request":true,"retention_days":30,"max_inference_tokens":512,"inference_provider":"cpu","entity_cache_enabled":true,"entity_cache_ttl_seconds":300}}`))
	settings.Header.Set("Content-Type", "application/json")
	settingsResponse := httptest.NewRecorder()
	handler.ServeHTTP(settingsResponse, settings)
	if settingsResponse.Code != http.StatusOK {
		t.Fatalf("enable raw request recording: %d %s", settingsResponse.Code, settingsResponse.Body.String())
	}
	body := gzipTestBody(t, `{"model":"custom-model","messages":[{"role":"user","content":"sk-test-1234567890123456"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/custom-model-endpoint", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("gzip proxy response: %d %s", response.Code, response.Body.String())
	}
	if upstreamEncoding != "gzip" {
		t.Fatalf("upstream content encoding = %q, want gzip", upstreamEncoding)
	}
	if strings.Contains(string(upstreamBody), "sk-test-1234567890123456") || !strings.Contains(string(upstreamBody), "<MASK_SECRET_KEY:") {
		t.Fatalf("upstream received unredacted gzip content: %s", upstreamBody)
	}

	logsResponse := httptest.NewRecorder()
	handler.ServeHTTP(logsResponse, httptest.NewRequest(http.MethodGet, "/api/v1/audit/logs?limit=1", nil))
	var listed struct {
		Logs []struct {
			ID string `json:"id"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(logsResponse.Body.Bytes(), &listed); err != nil || len(listed.Logs) != 1 {
		t.Fatalf("decode gzip audit list: %v body=%s", err, logsResponse.Body.String())
	}
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, httptest.NewRequest(http.MethodGet, "/api/v1/audit/logs/"+listed.Logs[0].ID, nil))
	var detail struct {
		Log struct {
			RawRequest *struct {
				Headers map[string][]string `json:"headers"`
				Body    string              `json:"body"`
			} `json:"raw_request"`
		} `json:"log"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode gzip audit detail: %v body=%s", err, detailResponse.Body.String())
	}
	if detail.Log.RawRequest == nil || !strings.Contains(detail.Log.RawRequest.Body, "sk-test-1234567890123456") || http.Header(detail.Log.RawRequest.Headers).Get("Content-Encoding") != "" {
		t.Fatalf("gzip raw request was not stored decoded: %#v body=%s", detail.Log.RawRequest, detailResponse.Body.String())
	}
}

func TestProtectedRouteRejectsInvalidGzipRequest(t *testing.T) {
	upstreamCalled := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamCalled = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader("not-gzip"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || upstreamCalled || !strings.Contains(response.Body.String(), "REQUEST_BODY_INVALID") {
		t.Fatalf("invalid gzip response=%d called=%v body=%s", response.Code, upstreamCalled, response.Body.String())
	}
}

func TestProtectedRouteRejectsOversizedDecompressedGzipRequest(t *testing.T) {
	upstreamCalled := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamCalled = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	body := gzipTestBody(t, `{"messages":[{"role":"user","content":"`+strings.Repeat("x", 9<<20)+`"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge || upstreamCalled || !strings.Contains(response.Body.String(), "REQUEST_BODY_INVALID") {
		t.Fatalf("oversized gzip response=%d called=%v body=%s", response.Code, upstreamCalled, response.Body.String())
	}
}

func TestRawRequestRecordingPersistsCompleteProxyExchange(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = io.ReadAll(r.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"raw response"}}]}`)),
			Request:    r,
		}, nil
	})}
	logger := log.New(io.Discard, "", 0)
	application, err := app.NewWithHTTPClient(logger, client)
	if err != nil {
		t.Fatal(err)
	}
	handler := combinedTestHandler(application.Handler(), application.ProxyHandler())
	configureUpstream(t, handler)

	settings := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"audit":{"record_request_content":true,"record_raw_request":true,"retention_days":30,"max_inference_tokens":512,"inference_provider":"cpu","entity_cache_enabled":true,"entity_cache_ttl_seconds":300}}`))
	settings.Header.Set("Content-Type", "application/json")
	settingsResponse := httptest.NewRecorder()
	handler.ServeHTTP(settingsResponse, settings)
	if settingsResponse.Code != http.StatusOK {
		t.Fatalf("enable raw request recording: %d %s", settingsResponse.Code, settingsResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"raw@example.com"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("proxy response: %d %s", response.Code, response.Body.String())
	}
	logsResponse := httptest.NewRecorder()
	handler.ServeHTTP(logsResponse, httptest.NewRequest(http.MethodGet, "/api/v1/audit/logs", nil))
	if logsResponse.Code != http.StatusOK || strings.Contains(logsResponse.Body.String(), `"raw_request"`) || strings.Contains(logsResponse.Body.String(), `"raw_response"`) ||
		strings.Contains(logsResponse.Body.String(), "raw@example.com") || strings.Contains(logsResponse.Body.String(), "raw response") {
		t.Fatalf("audit list included raw exchange: %d %s", logsResponse.Code, logsResponse.Body.String())
	}
	var listed struct {
		Logs []struct {
			ID string `json:"id"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(logsResponse.Body.Bytes(), &listed); err != nil || len(listed.Logs) != 1 {
		t.Fatalf("decode audit list: %v body=%s", err, logsResponse.Body.String())
	}
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, httptest.NewRequest(http.MethodGet, "/api/v1/audit/logs/"+listed.Logs[0].ID, nil))
	if detailResponse.Code != http.StatusOK || !strings.Contains(detailResponse.Body.String(), `"raw_request":{"method"`) || !strings.Contains(detailResponse.Body.String(), `"raw_response":{"status"`) ||
		!strings.Contains(detailResponse.Body.String(), "raw@example.com") || !strings.Contains(detailResponse.Body.String(), "raw response") {
		t.Fatalf("raw exchange was not persisted: %d %s", detailResponse.Code, detailResponse.Body.String())
	}
}

func TestJSONProxyDoesNotRedactAssistantOrSystemMessagesByDefault(t *testing.T) {
	var upstreamBody []byte
	handler := testHandlerWithClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamBody, _ = io.ReadAll(r.Body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)), Request: r}, nil
	})})
	configureUpstream(t, handler)

	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"system","content":"Contact system@example.com"},{"role":"assistant","content":"AI saw assistant@example.com"},{"role":"user","content":"secret sk-test-1234567890123456"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(string(upstreamBody), "system@example.com") || !strings.Contains(string(upstreamBody), "assistant@example.com") || strings.Contains(string(upstreamBody), "sk-test-1234567890123456") {
		t.Fatalf("unexpected upstream body: status=%d body=%s", response.Code, upstreamBody)
	}
}

func TestPolicyPartialUpdatesPreserveOtherFields(t *testing.T) {
	handler := testHandlerWithClient(t, http.DefaultClient)

	update := func(body string) pii.PolicySettings {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, "/api/v1/policy", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("update policy: %d %s", response.Code, response.Body.String())
		}
		var policy pii.PolicySettings
		if err := json.Unmarshal(response.Body.Bytes(), &policy); err != nil {
			t.Fatal(err)
		}
		return policy
	}

	policy := update(`{"redact_ai_answers":true}`)
	if !policy.Enabled || !policy.RedactAIAnswers || policy.RedactSystemMessages || len(policy.Rules) == 0 {
		t.Fatalf("AI history patch overwrote policy fields: %#v", policy)
	}
	policy = update(`{"redact_system_messages":true}`)
	if !policy.Enabled || !policy.RedactAIAnswers || !policy.RedactSystemMessages || len(policy.Rules) == 0 {
		t.Fatalf("system patch overwrote policy fields: %#v", policy)
	}
}

func TestJSONProxyRedactsSystemMessagesWhenEnabled(t *testing.T) {
	var upstreamBody []byte
	handler := testHandlerWithClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamBody, _ = io.ReadAll(r.Body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)), Request: r}, nil
	})})
	configureUpstream(t, handler)
	policy := httptest.NewRequest(http.MethodPut, "/api/v1/policy", strings.NewReader(`{"enabled":true,"redact_system_messages":true,"entity_types":[],"rules":[{"id":"email","pattern":"(?i)\\b[A-Z0-9._%+\\-]+@[A-Z0-9.\\-]+\\.[A-Z]{2,}\\b","enabled":true}]}`))
	policy.Header.Set("Content-Type", "application/json")
	policyResponse := httptest.NewRecorder()
	handler.ServeHTTP(policyResponse, policy)
	if policyResponse.Code != http.StatusOK {
		t.Fatalf("configure policy: %d %s", policyResponse.Code, policyResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/custom-completions", strings.NewReader(`{"model":"custom-model","instructions":"Contact instructions@example.com","system":"Contact top-level@example.com","messages":[{"role":"system","content":"Contact role@example.com"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || strings.Contains(string(upstreamBody), "@example.com") || strings.Count(string(upstreamBody), "<MASK_EMAIL:") != 3 {
		t.Fatalf("unexpected upstream body: status=%d body=%s", response.Code, upstreamBody)
	}
}

func TestJSONProxyRedactsAIAnswersWhenEnabled(t *testing.T) {
	var upstreamBody []byte
	handler := testHandlerWithClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamBody, _ = io.ReadAll(r.Body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)), Request: r}, nil
	})})
	configure := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams", strings.NewReader(`{"id":"mock","base_url":"https://mock.example","profile_id":"openai","credential_mode":"passthrough","enabled":true}`))
	configure.Header.Set("Content-Type", "application/json")
	configureResponse := httptest.NewRecorder()
	handler.ServeHTTP(configureResponse, configure)
	if configureResponse.Code != http.StatusCreated {
		t.Fatalf("configure upstream: %d %s", configureResponse.Code, configureResponse.Body.String())
	}
	policy := httptest.NewRequest(http.MethodPut, "/api/v1/policy", strings.NewReader(`{"enabled":true,"redact_ai_answers":true,"entity_types":[],"rules":[{"id":"email","pattern":"(?i)\\b[A-Z0-9._%+\\-]+@[A-Z0-9.\\-]+\\.[A-Z]{2,}\\b","enabled":true}]}`))
	policy.Header.Set("Content-Type", "application/json")
	policyResponse := httptest.NewRecorder()
	handler.ServeHTTP(policyResponse, policy)
	if policyResponse.Code != http.StatusOK {
		t.Fatalf("configure policy: %d %s", policyResponse.Code, policyResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"assistant","content":"assistant@example.com"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || strings.Contains(string(upstreamBody), "assistant@example.com") || !strings.Contains(string(upstreamBody), "<MASK_EMAIL:") {
		t.Fatalf("unexpected upstream body: status=%d body=%s", response.Code, upstreamBody)
	}
}

func TestManagementAndProxyRoutesAreSeparated(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	application, err := app.New(logger)
	if err != nil {
		t.Fatal(err)
	}

	managementProxy := httptest.NewRecorder()
	application.Handler().ServeHTTP(managementProxy, httptest.NewRequest(http.MethodPost, "/proxy/example/v1/chat/completions", nil))
	if managementProxy.Code != http.StatusNotFound {
		t.Fatalf("management handler proxy status = %d, want 404", managementProxy.Code)
	}

	proxyAPI := httptest.NewRecorder()
	application.ProxyHandler().ServeHTTP(proxyAPI, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if proxyAPI.Code != http.StatusNotFound {
		t.Fatalf("proxy handler API status = %d, want 404", proxyAPI.Code)
	}
}

func TestManagedAPIKeyUsesProfileHeaderTemplate(t *testing.T) {
	var authorization string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		authorization = r.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configure := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams", strings.NewReader(`{"id":"managed","base_url":"https://mock.example","profile_id":"openai","credential_mode":"managed","api_key":"sk-secret","enabled":true}`))
	configure.Header.Set("Content-Type", "application/json")
	configureResponse := httptest.NewRecorder()
	handler.ServeHTTP(configureResponse, configure)
	if configureResponse.Code != http.StatusCreated || strings.Contains(configureResponse.Body.String(), "sk-secret") {
		t.Fatalf("configure managed upstream: %d %s", configureResponse.Code, configureResponse.Body.String())
	}

	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(proxyResponse, httptest.NewRequest(http.MethodPost, "/proxy/managed/v1/chat/completions", nil))
	if proxyResponse.Code != http.StatusOK || authorization != "Bearer sk-secret" {
		t.Fatalf("proxy response=%d authorization=%q body=%s", proxyResponse.Code, authorization, proxyResponse.Body.String())
	}
}

func TestManagedCredentialRequiresAPIKey(t *testing.T) {
	handler := testHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams", strings.NewReader(`{"id":"managed","base_url":"https://mock.example","profile_id":"openai","credential_mode":"managed"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "api_key") {
		t.Fatalf("managed credential response=%d body=%s", response.Code, response.Body.String())
	}
}

func TestJSONProxyUsesStableTokensAcrossIndependentRequests(t *testing.T) {
	var tokens []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		tokens = append(tokens, extractToken(string(body)))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)),
			Request:    r,
		}, nil
	})}

	handler := testHandlerWithClient(t, client)
	configure := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams", strings.NewReader(`{"id":"mock","base_url":"https://mock.example","profile_id":"openai","credential_mode":"passthrough","enabled":true}`))
	configure.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), configure)

	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"密钥 sk-test-1234567890123456"}]}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("proxy response: %d %s", response.Code, response.Body.String())
		}
	}
	if len(tokens) != 2 || tokens[0] == "" || tokens[0] != tokens[1] {
		t.Fatalf("expected stable tokens across requests, got %#v", tokens)
	}
}

func TestSSEProxyRestoresTokenAcrossEvents(t *testing.T) {
	var upstreamBody []byte
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamBody, _ = io.ReadAll(r.Body)
		token := extractToken(string(upstreamBody))
		split := len(token) / 2
		body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"请联系 " + token[:split] + "\"}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + token[split:] + "，谢谢\"}}]}\n\n" +
			"data: [DONE]\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}

	handler := testHandlerWithClient(t, client)
	configure := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams", strings.NewReader(`{"id":"mock","base_url":"https://mock.example","profile_id":"openai","credential_mode":"passthrough","enabled":true}`))
	configure.Header.Set("Content-Type", "application/json")
	configureResponse := httptest.NewRecorder()
	handler.ServeHTTP(configureResponse, configure)

	proxyRequest := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"密钥 sk-test-1234567890123456"}],"stream":true}`))
	proxyRequest.Header.Set("Content-Type", "application/json")
	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(proxyResponse, proxyRequest)
	if proxyResponse.Code != http.StatusOK {
		t.Fatalf("proxy response: %d %s", proxyResponse.Code, proxyResponse.Body.String())
	}
	response := proxyResponse.Body.String()
	if !strings.Contains(response, "sk-test-1234567890123456") || strings.Contains(response, "<MASK_SECRET_KEY:") {
		t.Fatalf("stream was not restored: %s", response)
	}
	if !strings.Contains(response, "data: [DONE]") {
		t.Fatalf("terminal event missing: %s", response)
	}
}

func TestUnmatchedPathIsForwardedWithoutTransformation(t *testing.T) {
	requestBody := `{"payload":"电话 13800138000","nested":{"email":"test@example.com"}}`
	responseBody := `{"echo":"电话 13800138000","token":"<PHONE_NUMBER:ABCD>"}`
	var receivedBody, receivedPath, receivedQuery string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		receivedBody, receivedPath, receivedQuery = string(body), r.URL.Path, r.URL.RawQuery
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Upstream": []string{"preserved"}},
			Body:       io.NopCloser(strings.NewReader(responseBody)),
			Request:    r,
		}, nil
	})}

	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/files?purpose=fine-tune&trace=abc", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || response.Body.String() != responseBody {
		t.Fatalf("passthrough response = %d %q", response.Code, response.Body.String())
	}
	if receivedBody != requestBody || receivedPath != "/v1/files" || receivedQuery != "purpose=fine-tune&trace=abc" {
		t.Fatalf("upstream received body=%q path=%q query=%q", receivedBody, receivedPath, receivedQuery)
	}
	if response.Header().Get("X-Upstream") != "preserved" {
		t.Fatalf("upstream response header was not preserved: %#v", response.Header())
	}

	logs := httptest.NewRecorder()
	handler.ServeHTTP(logs, httptest.NewRequest(http.MethodGet, "/api/v1/audit/logs?limit=1", nil))
	if strings.Contains(logs.Body.String(), "13800138000") || !strings.Contains(logs.Body.String(), `"protection_mode":"passthrough"`) || !strings.Contains(logs.Body.String(), `"operation_id":"passthrough"`) {
		t.Fatalf("unexpected passthrough audit log: %s", logs.Body.String())
	}
}

func TestUnmatchedModelPathUsesGenericStrategy(t *testing.T) {
	var receivedBody string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		token := extractToken(receivedBody)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{` +
				`"choices":[{"message":{"content":"` + token + `"}}],` +
				`"content":[{"text":"` + token + `"}],` +
				`"candidates":[{"content":{"parts":[{"text":"` + token + `"}]}}],` +
				`"output":[{"content":[{"text":"` + token + `"}]}]` +
				`}`)),
			Request: r,
		}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/custom-completions", strings.NewReader(`{`+
		`"model":"custom-model",`+
		`"instructions":"OpenAI sk-test-1234567890123456",`+
		`"messages":[{"role":"user","content":"Anthropic sk-test-1234567890123456"}],`+
		`"system":"System sk-test-1234567890123456",`+
		`"contents":[{"role":"user","parts":[{"text":"Gemini sk-test-1234567890123456"}]}]`+
		`}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || strings.Count(receivedBody, "sk-test-1234567890123456") != 2 || !strings.Contains(receivedBody, "<MASK_SECRET_KEY:") {
		t.Fatalf("generic fallback did not redact request: status=%d body=%q", response.Code, receivedBody)
	}
	if strings.Count(response.Body.String(), "sk-test-1234567890123456") != 4 || strings.Contains(response.Body.String(), "<MASK_SECRET_KEY:") {
		t.Fatalf("generic fallback did not restore response: %s", response.Body.String())
	}

	logs := httptest.NewRecorder()
	handler.ServeHTTP(logs, httptest.NewRequest(http.MethodGet, "/api/v1/audit/logs?limit=1", nil))
	if !strings.Contains(logs.Body.String(), `"operation_id":"generic-model-request"`) || !strings.Contains(logs.Body.String(), `"model":"custom-model"`) || !strings.Contains(logs.Body.String(), `"protection_mode":"redacted"`) {
		t.Fatalf("generic fallback audit entry missing: %s", logs.Body.String())
	}
}

func TestAutoRouteUsesGenericStrategyForUnmatchedModelPath(t *testing.T) {
	var receivedHost, receivedBody string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		receivedHost, receivedBody = r.URL.Host, string(body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/custom-model-endpoint", strings.NewReader(`{"model":"custom-model","messages":[{"role":"user","content":"sk-test-1234567890123456"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || receivedHost != "mock.example" || strings.Contains(receivedBody, "sk-test-1234567890123456") {
		t.Fatalf("auto generic fallback failed: status=%d host=%q body=%q", response.Code, receivedHost, receivedBody)
	}
}

func TestMatchedInvalidJSONIsForwardedWithoutBlocking(t *testing.T) {
	const requestBody = `{"messages":[broken`
	var received string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || received != requestBody {
		t.Fatalf("invalid JSON was blocked or changed: status=%d received=%q body=%s", response.Code, received, response.Body.String())
	}
}

func TestOversizedMatchedBodyIsStreamedAsPassthrough(t *testing.T) {
	requestBody := `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 9<<20) + `foo@example.com"}]}`
	var receivedBytes int
	var receivedTail string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		receivedBytes = len(body)
		if len(body) > 32 {
			receivedTail = string(body[len(body)-32:])
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || receivedBytes != len(requestBody) || !strings.Contains(receivedTail, "foo@example.com") {
		t.Fatalf("oversized body was blocked or truncated: status=%d bytes=%d/%d tail=%q", response.Code, receivedBytes, len(requestBody), receivedTail)
	}
}

func TestOversizedProtectedResponseIsStreamedWithoutBlocking(t *testing.T) {
	responseBody := `{"payload":"` + strings.Repeat("y", 9<<20) + `"}`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"foo@example.com"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.Len() != len(responseBody) {
		t.Fatalf("oversized response was blocked or truncated: status=%d bytes=%d/%d", response.Code, response.Body.Len(), len(responseBody))
	}
}

func TestUnmatchedPathStreamsSSEWithoutTransformation(t *testing.T) {
	streamBody := "event: message\ndata: {\"value\":\"<PHONE_NUMBER:ABCD>\"}\n\ndata: [DONE]\n\n"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(streamBody)),
			Request:    r,
		}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodGet, "/proxy/mock/v1/events", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != streamBody {
		t.Fatalf("SSE passthrough response = %d %q", response.Code, response.Body.String())
	}
}

func TestUnconfiguredUpstreamStillReturnsNotFound(t *testing.T) {
	handler := testHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/proxy/missing/v1/models", nil))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "UPSTREAM_NOT_FOUND") {
		t.Fatalf("unconfigured upstream response = %d %s", response.Code, response.Body.String())
	}
}

func TestDisabledUpstreamIsUnavailableToGateway(t *testing.T) {
	handler := testHandler(t)
	configure := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams", strings.NewReader(`{"id":"disabled","base_url":"https://disabled.example","profile_id":"openai","credential_mode":"passthrough","enabled":false}`))
	configure.Header.Set("Content-Type", "application/json")
	configured := httptest.NewRecorder()
	handler.ServeHTTP(configured, configure)
	if configured.Code != http.StatusCreated || !strings.Contains(configured.Body.String(), `"enabled":false`) {
		t.Fatalf("configure disabled upstream = %d %s", configured.Code, configured.Body.String())
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/proxy/disabled/v1/models", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "UPSTREAM_DISABLED") {
		t.Fatalf("disabled upstream response = %d %s", response.Code, response.Body.String())
	}
}

func TestProxyResolvesConfiguredServiceByDomain(t *testing.T) {
	var receivedPath string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		receivedPath = r.URL.Path
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/proxy/mock.example/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"电话 13800138000"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || receivedPath != "/v1/chat/completions" {
		t.Fatalf("domain route response=%d path=%q body=%s", response.Code, receivedPath, response.Body.String())
	}
}

func TestProxyAutoMatchesUniqueConfiguredService(t *testing.T) {
	var receivedHost string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		receivedHost = r.URL.Host
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || receivedHost != "mock.example" {
		t.Fatalf("auto route response=%d host=%q body=%s", response.Code, receivedHost, response.Body.String())
	}
}

func TestProxyAutoRoutesModelListAsPassthrough(t *testing.T) {
	const upstreamResponse = `{"object":"list","data":[{"id":"model-1"}]}`
	var receivedHost, receivedPath string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		receivedHost, receivedPath = r.URL.Host, r.URL.Path
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(upstreamResponse)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if response.Code != http.StatusOK || response.Body.String() != upstreamResponse || receivedHost != "mock.example" || receivedPath != "/v1/models" {
		t.Fatalf("model list response=%d host=%q path=%q body=%s", response.Code, receivedHost, receivedPath, response.Body.String())
	}

	logs := httptest.NewRecorder()
	handler.ServeHTTP(logs, httptest.NewRequest(http.MethodGet, "/api/v1/audit/logs?limit=1", nil))
	if !strings.Contains(logs.Body.String(), `"operation_id":"list-models"`) || !strings.Contains(logs.Body.String(), `"protection_mode":"passthrough"`) {
		t.Fatalf("unexpected model list audit log: %s", logs.Body.String())
	}
}

func TestProxyAutoRouteUsesFirstMatchingService(t *testing.T) {
	var receivedHost string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		receivedHost = r.URL.Host
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)), Request: r}, nil
	})}
	handler := testHandlerWithClient(t, client)
	configureUpstream(t, handler)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams", strings.NewReader(`{"id":"aaa-first","base_url":"https://first.example","profile_id":"openai","credential_mode":"passthrough","enabled":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("configure second upstream: %d %s", response.Code, response.Body.String())
	}
	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(proxyResponse, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if proxyResponse.Code != http.StatusOK || receivedHost != "first.example" {
		t.Fatalf("first auto route response=%d host=%q body=%s", proxyResponse.Code, receivedHost, proxyResponse.Body.String())
	}
}

func configureUpstream(t *testing.T, handler http.Handler) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams", strings.NewReader(`{"id":"mock","base_url":"https://mock.example","profile_id":"openai","credential_mode":"passthrough","enabled":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("configure upstream: %d %s", response.Code, response.Body.String())
	}
}

func gzipTestBody(t *testing.T, body string) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func testHandler(t *testing.T) http.Handler {
	return testHandlerWithClient(t, nil)
}

func testHandlerWithClient(t *testing.T, client *http.Client) http.Handler {
	t.Helper()
	logger := log.New(io.Discard, "", 0)
	application, err := app.NewWithHTTPClient(logger, client)
	if err != nil {
		t.Fatal(err)
	}
	return combinedTestHandler(application.Handler(), application.ProxyHandler())
}

func combinedTestHandler(api, proxy http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
		} else {
			proxy.ServeHTTP(w, r)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func extractToken(body string) string {
	start := strings.Index(body, "<MASK_SECRET_KEY:")
	if start < 0 {
		return ""
	}
	end := strings.Index(body[start:], ">")
	if end < 0 {
		return ""
	}
	return body[start : start+end+1]
}
