package forwardproxy_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/remask/remask-core/internal/app"
	"github.com/remask/remask-core/internal/forwardproxy"
)

func TestConfiguredHTTPSUpstreamIsRedactedAndRestored(t *testing.T) {
	var received string
	upstreamServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		token := extractRuleToken(received)
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
	request, _ := http.NewRequest(http.MethodPost, upstreamServer.URL+"/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"secret sk-test-1234567890123456"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(received, "sk-test-1234567890123456") || !strings.Contains(received, "<MASK_SECRET_KEY:") {
		t.Fatalf("upstream received unprotected body: %s", received)
	}
	if !strings.Contains(string(body), "sk-test-1234567890123456") || strings.Contains(string(body), "<MASK_SECRET_KEY:") {
		t.Fatalf("response was not restored: %s", body)
	}
	if !strings.Contains(logs.String(), `forward_proxy_request method=CONNECT target="`+request.URL.Host+`" path="-"`) ||
		!strings.Contains(logs.String(), `forward_proxy_request method=POST target="`+request.URL.Host+`" path="/v1/chat/completions"`) {
		t.Fatalf("HTTPS proxy requests were not logged: %q", logs.String())
	}
	auditResponse := httptest.NewRecorder()
	application.Handler().ServeHTTP(auditResponse, httptest.NewRequest(http.MethodGet, "/api/v1/audit/logs?limit=1", nil))
	parsedUpstream, _ := url.Parse(upstreamServer.URL)
	if auditResponse.Code != http.StatusOK ||
		!strings.Contains(auditResponse.Body.String(), `"gateway_type":"proxy_gateway"`) ||
		!strings.Contains(auditResponse.Body.String(), `"upstream_id":"test"`) ||
		!strings.Contains(auditResponse.Body.String(), `"target_host":"`+parsedUpstream.Hostname()+`"`) {
		t.Fatalf("audit log did not identify the proxy gateway: %d %s", auditResponse.Code, auditResponse.Body.String())
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

func TestProxyGatewayServesHTTPAndSOCKS5OnSamePort(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "shared proxy gateway")
	}))
	defer upstreamServer.Close()
	application := newTestApp(t, nil)
	proxyAddress := startProxyGateway(t, application)

	proxyURL, _ := url.Parse("http://" + proxyAddress)
	httpClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	httpResponse, err := httpClient.Get(upstreamServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	httpBody, _ := io.ReadAll(httpResponse.Body)
	_ = httpResponse.Body.Close()
	if string(httpBody) != "shared proxy gateway" {
		t.Fatalf("unexpected HTTP proxy body: %q", httpBody)
	}

	upstreamURL, _ := url.Parse(upstreamServer.URL)
	_, upstreamPort, _ := net.SplitHostPort(upstreamURL.Host)
	socksConnection := dialSOCKS5(t, proxyAddress, net.JoinHostPort("localhost", upstreamPort))
	defer socksConnection.Close()
	_, _ = fmt.Fprintf(socksConnection, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", upstreamURL.Host)
	socksResponse, err := http.ReadResponse(bufio.NewReader(socksConnection), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	socksBody, _ := io.ReadAll(socksResponse.Body)
	_ = socksResponse.Body.Close()
	if string(socksBody) != "shared proxy gateway" {
		t.Fatalf("unexpected SOCKS5 proxy body: %q", socksBody)
	}
}

func TestConfiguredHTTPSUpstreamIsProtectedOverSOCKS5(t *testing.T) {
	var received string
	upstreamServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		token := extractRuleToken(received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"restored `+token+`"}}]}`)
	}))
	defer upstreamServer.Close()

	application := newTestApp(t, upstreamServer.Client())
	configureProxyRule(t, application.Handler(), upstreamServer.URL)
	proxyAddress := startProxyGateway(t, application)
	upstreamURL, _ := url.Parse(upstreamServer.URL)

	connection := dialSOCKS5(t, proxyAddress, upstreamURL.Host)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(application.RootCertificatePEM())
	tlsConnection := tls.Client(connection, &tls.Config{RootCAs: roots, ServerName: upstreamURL.Hostname(), MinVersion: tls.VersionTLS12})
	if err := tlsConnection.Handshake(); err != nil {
		t.Fatal(err)
	}
	defer tlsConnection.Close()
	payload := `{"messages":[{"role":"user","content":"secret sk-socks-1234567890123456"}]}`
	_, _ = fmt.Fprintf(tlsConnection, "POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", upstreamURL.Host, len(payload), payload)
	response, err := http.ReadResponse(bufio.NewReader(tlsConnection), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if strings.Contains(received, "sk-socks-1234567890123456") || !strings.Contains(received, "<MASK_SECRET_KEY:") {
		t.Fatalf("SOCKS5 upstream received unprotected body: %s", received)
	}
	if !strings.Contains(string(body), "sk-socks-1234567890123456") || strings.Contains(string(body), "<MASK_SECRET_KEY:") {
		t.Fatalf("SOCKS5 response was not restored: %s", body)
	}
}

func TestWildcardHTTPSRuleIsInspected(t *testing.T) {
	upstreamServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "wildcard inspected")
	}))
	defer upstreamServer.Close()

	application := newTestApp(t, upstreamServer.Client())
	parsed, _ := url.Parse(upstreamServer.URL)
	port := 443
	if parsed.Port() != "" {
		_, _ = fmt.Sscanf(parsed.Port(), "%d", &port)
	}
	body, _ := json.Marshal(map[string]any{
		"id": "wildcard", "hosts": []string{fmt.Sprintf("*:%d", port)}, "profile_id": "openai", "enabled": true,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/proxy-rules", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("configure wildcard proxy rule: %d %s", response.Code, response.Body.String())
	}

	proxyServer := httptest.NewServer(application.ForwardProxyHandler())
	defer proxyServer.Close()
	client := inspectedClient(t, proxyServer.URL, application.RootCertificatePEM())
	upstreamResponse, err := client.Get(upstreamServer.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer upstreamResponse.Body.Close()
	responseBody, _ := io.ReadAll(upstreamResponse.Body)
	if string(responseBody) != "wildcard inspected" {
		t.Fatalf("unexpected wildcard response: %q", responseBody)
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

func startProxyGateway(t *testing.T, application *app.App) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &forwardproxy.Server{Handler: application.ForwardProxy(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("proxy gateway did not stop")
		}
	})
	return listener.Addr().String()
}

func dialSOCKS5(t *testing.T, proxyAddress, targetAuthority string) net.Conn {
	t.Helper()
	connection, err := net.DialTimeout("tcp", proxyAddress, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(err error) net.Conn {
		_ = connection.Close()
		t.Fatal(err)
		return nil
	}
	if _, err := connection.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return fail(err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(connection, greeting); err != nil {
		return fail(err)
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		return fail(fmt.Errorf("SOCKS5 greeting response = %v", greeting))
	}
	host, portText, err := net.SplitHostPort(targetAuthority)
	if err != nil {
		return fail(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return fail(err)
	}
	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		request = append(request, 0x01)
		request = append(request, ip.To4()...)
	} else {
		if len(host) > 255 {
			return fail(errors.New("SOCKS5 test target is too long"))
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	if _, err := connection.Write(request); err != nil {
		return fail(err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return fail(err)
	}
	if header[1] != 0x00 {
		return fail(fmt.Errorf("SOCKS5 CONNECT reply = %d", header[1]))
	}
	addressLength := 0
	switch header[3] {
	case 0x01:
		addressLength = net.IPv4len
	case 0x04:
		addressLength = net.IPv6len
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(connection, length); err != nil {
			return fail(err)
		}
		addressLength = int(length[0])
	default:
		return fail(fmt.Errorf("SOCKS5 reply address type = %d", header[3]))
	}
	if _, err := io.ReadFull(connection, make([]byte, addressLength+2)); err != nil {
		return fail(err)
	}
	return connection
}

func configureProxyRule(t *testing.T, handler http.Handler, targetURL string) {
	t.Helper()
	parsed, _ := url.Parse(targetURL)
	port := 443
	if parsed.Port() != "" {
		_, _ = fmt.Sscanf(parsed.Port(), "%d", &port)
	}
	body, _ := json.Marshal(map[string]any{
		"id": "test", "hosts": []string{net.JoinHostPort(parsed.Hostname(), strconv.Itoa(port))}, "profile_id": "openai", "enabled": true,
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

func extractRuleToken(body string) string {
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
