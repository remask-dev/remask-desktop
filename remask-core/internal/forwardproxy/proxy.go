package forwardproxy

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/remask/remask-core/internal/gateway"
	"github.com/remask/remask-core/internal/mitm"
	"github.com/remask/remask-core/internal/proxyrule"
)

const connectTimeout = 15 * time.Second

// Proxy is an explicit HTTP proxy. HTTPS destinations that match a configured
// upstream are inspected locally; all other CONNECT requests are byte-for-byte
// tunnels and never receive a Remask-issued certificate.
type Proxy struct {
	logger    *log.Logger
	rules     *proxyrule.Registry
	gateway   *gateway.Gateway
	authority *mitm.Authority
	transport *http.Transport
	dialer    net.Dialer
}

func New(logger *log.Logger, rules *proxyrule.Registry, gateway *gateway.Gateway, authority *mitm.Authority) *Proxy {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Proxy{
		logger: logger, rules: rules, gateway: gateway, authority: authority,
		transport: transport,
		dialer:    net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second},
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.logRequest(r)

	if r.Method == http.MethodConnect {
		p.serveConnect(w, r)
		return
	}

	rule, ok := p.rules.MatchAuthority(requestRuleAuthority(r))
	if ok {
		targetBaseURL := requestTargetBaseURL(r, "http")
		if isUpgrade(r) {
			p.reverseUnmodified(w, r, targetBaseURL)
			return
		}
		p.gateway.ServeProxyHTTP(w, r, rule.ID, rule.ProfileID, targetBaseURL)
		return
	}
	if isUpgrade(r) {
		p.reverseUnmodified(w, r, r.URL.Scheme+"://"+r.URL.Host)
		return
	}
	p.forwardUnconfigured(w, r)
}

func (p *Proxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	authority := connectAuthority(r.Host)
	rule, inspect := p.rules.MatchAuthority(authority)
	if !inspect {
		p.tunnel(w, r, authority)
		return
	}
	p.inspectTLS(w, r, authority, rule)
}

func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request, authority string) {
	client, ok := hijack(w)
	if !ok {
		http.Error(w, "proxy connection hijacking is unavailable", http.StatusInternalServerError)
		return
	}
	upstreamConn, err := p.dialer.DialContext(r.Context(), "tcp", authority)
	if err != nil {
		_ = writeConnectError(client, http.StatusBadGateway)
		_ = client.Close()
		return
	}
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = upstreamConn.Close()
		return
	}
	splice(client, upstreamConn)
}

func (p *Proxy) inspectTLS(w http.ResponseWriter, r *http.Request, authority string, rule proxyrule.Rule) {
	host := authorityHostname(authority)
	certificate, err := p.authority.CertificateFor(host)
	if err != nil {
		http.Error(w, "failed to create local TLS certificate", http.StatusInternalServerError)
		return
	}
	client, ok := hijack(w)
	if !ok {
		http.Error(w, "proxy connection hijacking is unavailable", http.StatusInternalServerError)
		return
	}
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		return
	}

	tlsConn := tls.Server(client, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	})
	if err := tlsConn.HandshakeContext(r.Context()); err != nil {
		p.logger.Printf("forward_proxy_tls_handshake_failed host=%s error=%v", host, err)
		_ = tlsConn.Close()
		return
	}

	listener := newSingleConnListener(tlsConn)
	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			p.logRequest(request)
			if !sameHostname(request.Host, host) {
				http.Error(response, "CONNECT target and request host differ", http.StatusMisdirectedRequest)
				return
			}
			if isUpgrade(request) {
				p.reverseUnmodified(response, request, "https://"+authority)
				return
			}
			p.gateway.ServeProxyHTTP(response, request, rule.ID, rule.ProfileID, "https://"+authority)
		}),
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		p.logger.Printf("forward_proxy_tls_server_failed host=%s error=%v", host, err)
	}
}

func requestTargetBaseURL(r *http.Request, fallbackScheme string) string {
	scheme := fallbackScheme
	host := r.Host
	if r.URL != nil {
		if r.URL.Scheme != "" {
			scheme = r.URL.Scheme
		}
		if r.URL.Host != "" {
			host = r.URL.Host
		}
	}
	return scheme + "://" + host
}

func (p *Proxy) reverseUnmodified(w http.ResponseWriter, r *http.Request, baseURL string) {
	target, err := url.Parse(baseURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		http.Error(w, "invalid proxy target", http.StatusBadGateway)
		return
	}
	reverse := httputil.NewSingleHostReverseProxy(target)
	reverse.Transport = p.transport
	reverse.FlushInterval = -1
	reverse.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, proxyErr error) {
		p.logger.Printf("forward_proxy_upgrade_failed target=%s error=%v", target.Host, proxyErr)
		http.Error(response, proxyErr.Error(), http.StatusBadGateway)
	}
	originalDirector := reverse.Director
	reverse.Director = func(request *http.Request) {
		originalDirector(request)
		removeProxyHeaders(request.Header)
		request.Host = target.Host
	}
	reverse.ServeHTTP(w, r)
}

func isUpgrade(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Upgrade")) == "" {
		return false
	}
	for _, value := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(value), "upgrade") {
			return true
		}
	}
	return false
}

func (p *Proxy) forwardUnconfigured(w http.ResponseWriter, r *http.Request) {
	request := r.Clone(r.Context())
	request.RequestURI = ""
	if request.URL.Scheme == "" {
		request.URL.Scheme = "http"
	}
	if request.URL.Host == "" {
		request.URL.Host = request.Host
	}
	removeProxyHeaders(request.Header)
	response, err := p.transport.RoundTrip(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	copyHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func requestAuthority(r *http.Request) string {
	if r.URL != nil && r.URL.Host != "" {
		return r.URL.Host
	}
	return r.Host
}

func requestRuleAuthority(r *http.Request) string {
	authority := requestAuthority(r)
	parsed, err := url.Parse("//" + authority)
	if err != nil || parsed.Hostname() == "" || parsed.Port() != "" {
		return authority
	}
	port := "80"
	if r.URL != nil && strings.EqualFold(r.URL.Scheme, "https") {
		port = "443"
	}
	return net.JoinHostPort(parsed.Hostname(), port)
}

func (p *Proxy) logRequest(r *http.Request) {
	p.logger.Printf(
		"forward_proxy_request method=%s target=%q path=%q",
		r.Method,
		requestAuthority(r),
		requestLogPath(r),
	)
}

func requestLogPath(r *http.Request) string {
	if r.Method == http.MethodConnect {
		return "-"
	}
	if r.URL == nil || r.URL.Path == "" {
		return "/"
	}
	return r.URL.EscapedPath()
}

func connectAuthority(authority string) string {
	authority = strings.TrimSpace(authority)
	if _, _, err := net.SplitHostPort(authority); err == nil {
		return authority
	}
	return net.JoinHostPort(strings.Trim(authority, "[]"), "443")
}

func authorityHostname(authority string) string {
	host, _, err := net.SplitHostPort(authority)
	if err != nil {
		return strings.Trim(strings.ToLower(authority), "[] .")
	}
	return strings.Trim(strings.ToLower(host), "[] .")
}

func sameHostname(authority, expected string) bool {
	return authorityHostname(authority) == authorityHostname(expected)
}

func hijack(w http.ResponseWriter) (net.Conn, bool) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, false
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		return nil, false
	}
	if buffered.Reader.Buffered() > 0 {
		// CONNECT requests must not carry tunneled bytes before the 200 response.
		_ = connection.Close()
		return nil, false
	}
	return connection, true
}

func splice(left, right net.Conn) {
	var wait sync.WaitGroup
	copyOne := func(destination, source net.Conn) {
		defer wait.Done()
		_, _ = io.Copy(destination, source)
		if tcp, ok := destination.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}
	wait.Add(2)
	go copyOne(left, right)
	go copyOne(right, left)
	wait.Wait()
	_ = left.Close()
	_ = right.Close()
}

func writeConnectError(connection net.Conn, status int) error {
	_, err := io.WriteString(connection, "HTTP/1.1 "+strconv.Itoa(status)+" "+http.StatusText(status)+"\r\nConnection: close\r\n\r\n")
	return err
}

func removeProxyHeaders(header http.Header) {
	header.Del("Proxy-Authorization")
	header.Del("Proxy-Connection")
}

func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

type singleConnListener struct {
	connection net.Conn
	once       sync.Once
	done       chan struct{}
	closed     sync.Once
}

func newSingleConnListener(connection net.Conn) *singleConnListener {
	listener := &singleConnListener{done: make(chan struct{})}
	listener.connection = &notifyingConn{Conn: connection, closed: listener.closeDone}
	return listener
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	accepted := false
	l.once.Do(func() { accepted = true })
	if accepted {
		return l.connection, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	l.closeDone()
	return l.connection.Close()
}

func (l *singleConnListener) Addr() net.Addr { return l.connection.LocalAddr() }
func (l *singleConnListener) closeDone()     { l.closed.Do(func() { close(l.done) }) }

type notifyingConn struct {
	net.Conn
	closed func()
}

func (c *notifyingConn) Close() error {
	c.closed()
	return c.Conn.Close()
}
