package forwardproxy_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/remask/remask-core/internal/app"
)

func TestConfiguredHTTPSUpstreamIsRedactedAndRestored(t *testing.T) {
	var received string
	upstreamServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		token := extractEmailToken(received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"restored `+token+`"}}]}`)
	}))
	defer upstreamServer.Close()

	var logs bytes.Buffer
	application, err := app.NewWithHTTPClient(log.New(&logs, "", 0), upstreamServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	configureProxyRule(t, application.Handler(), upstreamServer.URL)
	proxyServer := httptest.NewServer(application.ForwardProxyHandler())
	defer proxyServer.Close()

	client := inspectedClient(t, proxyServer.URL, application.RootCertificatePEM())
	request, _ := http.NewRequest(http.MethodPost, upstreamServer.URL+"/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"email foo@example.com"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(received, "foo@example.com") || !strings.Contains(received, "<MASK_EMAIL:") {
		t.Fatalf("upstream received unprotected body: %s", received)
	}
	if !strings.Contains(string(body), "foo@example.com") || strings.Contains(string(body), "<MASK_EMAIL:") {
		t.Fatalf("response was not restored: %s", body)
	}
	if !strings.Contains(logs.String(), `forward_proxy_request method=CONNECT target="`+request.URL.Host+`" path="-"`) ||
		!strings.Contains(logs.String(), `forward_proxy_request method=POST target="`+request.URL.Host+`" path="/v1/chat/completions"`) {
		t.Fatalf("HTTPS proxy requests were not logged: %q", logs.String())
	}
}

func TestConfiguredHTTPSUnmatchedPathPassesThrough(t *testing.T) {
	const payload = `{"unknown":"foo@example.com"}`
	var received string
	upstreamServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer upstreamServer.Close()

	application := newTestApp(t, upstreamServer.Client())
	configureProxyRule(t, application.Handler(), upstreamServer.URL)
	proxyServer := httptest.NewServer(application.ForwardProxyHandler())
	defer proxyServer.Close()

	client := inspectedClient(t, proxyServer.URL, application.RootCertificatePEM())
	request, _ := http.NewRequest(http.MethodPost, upstreamServer.URL+"/unmatched", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if received != payload || string(body) != payload {
		t.Fatalf("unmatched request changed: received=%q response=%q", received, body)
	}
}

func TestUnconfiguredHTTPSIsRawTunnel(t *testing.T) {
	upstreamServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "raw tunnel")
	}))
	defer upstreamServer.Close()
	application := newTestApp(t, nil)
	proxyServer := httptest.NewServer(application.ForwardProxyHandler())
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	transport := upstreamServer.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Transport: transport}
	response, err := client.Get(upstreamServer.URL + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "raw tunnel" {
		t.Fatalf("unexpected tunneled response: %q", body)
	}
}

func TestForwardProxyLogsHTTPRequestsWithoutQueryValues(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstreamServer.Close()

	var output bytes.Buffer
	application, err := app.NewWithHTTPClient(log.New(&output, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, upstreamServer.URL+"/safe/path?api_key=do-not-log", nil)
	response := httptest.NewRecorder()
	application.ForwardProxyHandler().ServeHTTP(response, request)

	logged := output.String()
	if !strings.Contains(logged, `forward_proxy_request method=GET target="`+request.URL.Host+`" path="/safe/path"`) {
		t.Fatalf("forward proxy request was not logged safely: %q", logged)
	}
	if strings.Contains(logged, "do-not-log") || strings.Contains(logged, "api_key") {
		t.Fatalf("forward proxy log exposed query data: %q", logged)
	}
}

func TestForwardProxyLogsConnectRequests(t *testing.T) {
	var output bytes.Buffer
	application, err := app.NewWithHTTPClient(log.New(&output, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodConnect, "http://proxy.invalid", nil)
	request.Host = "example.com:443"
	request.URL.Host = ""
	response := httptest.NewRecorder()
	application.ForwardProxyHandler().ServeHTTP(response, request)

	logged := output.String()
	if !strings.Contains(logged, `forward_proxy_request method=CONNECT target="example.com:443" path="-"`) {
		t.Fatalf("CONNECT request was not logged: %q", logged)
	}
}

func newTestApp(t *testing.T, upstreamClient *http.Client) *app.App {
	t.Helper()
	application, err := app.NewWithHTTPClient(log.New(io.Discard, "", 0), upstreamClient)
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func configureProxyRule(t *testing.T, handler http.Handler, targetURL string) {
	t.Helper()
	parsed, _ := url.Parse(targetURL)
	port := 443
	if parsed.Port() != "" {
		_, _ = fmt.Sscanf(parsed.Port(), "%d", &port)
	}
	body, _ := json.Marshal(map[string]any{
		"id": "test", "hosts": []string{parsed.Hostname()}, "port": port, "profile_id": "openai", "enabled": true,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/proxy-rules", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("configure proxy rule: %d %s", response.Code, response.Body.String())
	}
}

func inspectedClient(t *testing.T, proxyAddress string, rootPEM []byte) *http.Client {
	t.Helper()
	proxyURL, _ := url.Parse(proxyAddress)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		t.Fatal("append Remask root certificate")
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
}

func extractEmailToken(body string) string {
	start := strings.Index(body, "<MASK_EMAIL:")
	if start < 0 {
		return ""
	}
	end := strings.Index(body[start:], ">")
	if end < 0 {
		return ""
	}
	return body[start : start+end+1]
}
