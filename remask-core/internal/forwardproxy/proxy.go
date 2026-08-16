package forwardproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
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

// Proxy is an explicit HTTP and SOCKS5 proxy. HTTPS destinations that match a
// configured rule are inspected locally; all other connections are
// byte-for-byte tunnels and never receive a Remask-issued certificate.
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
	p.serveInspectedTLS(r.Context(), client, authority, rule, certificate)
}

func (p *Proxy) serveInspectedTLS(ctx context.Context, client net.Conn, authority string, rule proxyrule.Rule, certificate tls.Certificate) {
	host := authorityHostname(authority)
	tlsConn := tls.Server(client, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		p.logger.Printf("forward_proxy_tls_handshake_failed host=%s error=%v", host, err)
		_ = tlsConn.Close()
		return
	}
	p.serveInspectedHTTP(tlsConn, "https", authority, rule)
}

func (p *Proxy) serveInspectedHTTP(client net.Conn, scheme, authority string, rule proxyrule.Rule) {
	host := authorityHostname(authority)
	listener := newSingleConnListener(client)
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
				p.reverseUnmodified(response, request, scheme+"://"+authority)
				return
			}
			p.gateway.ServeProxyHTTP(response, request, rule.ID, rule.ProfileID, scheme+"://"+authority)
		}),
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		p.logger.Printf("forward_proxy_tls_server_failed host=%s error=%v", host, err)
	}
}

// ServeSOCKS serves one SOCKS5 connection. Only unauthenticated CONNECT is
// supported. DNS names are kept intact when the client uses remote DNS (for
// example socks5h://), allowing protected-target rules to match the hostname.
func (p *Proxy) ServeSOCKS(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(connectTimeout))
	reader := bufio.NewReader(client)
	connection := &bufferedConn{Conn: client, reader: reader}

	if err := negotiateSOCKS5(reader, client); err != nil {
		return
	}
	authority, err := readSOCKS5Connect(reader, client)
	if err != nil {
		return
	}
	p.logger.Printf("forward_proxy_request method=SOCKS5 target=%q path=%q", authority, "-")

	rule, inspect := p.rules.MatchAuthority(authority)
	if !inspect {
		p.tunnelSOCKS(connection, authority)
		return
	}

	if err := writeSOCKS5Reply(client, socksReplySucceeded, nil); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	first, err := reader.Peek(1)
	if err != nil {
		return
	}
	if first[0] != 0x16 {
		p.serveInspectedHTTP(connection, "http", authority, rule)
		return
	}

	certificate, err := p.authority.CertificateFor(authorityHostname(authority))
	if err != nil {
		return
	}
	p.serveInspectedTLS(context.Background(), connection, authority, rule, certificate)
}

func (p *Proxy) tunnelSOCKS(client net.Conn, authority string) {
	upstream, err := p.dialer.Dial("tcp", authority)
	if err != nil {
		_ = writeSOCKS5Reply(client, socksReplyHostUnreachable, nil)
		return
	}
	if err := writeSOCKS5Reply(client, socksReplySucceeded, upstream.LocalAddr()); err != nil {
		_ = upstream.Close()
		return
	}
	_ = client.SetDeadline(time.Time{})
	splice(client, upstream)
}

const (
	socksReplySucceeded       = byte(0x00)
	socksReplyGeneralFailure  = byte(0x01)
	socksReplyCommandRejected = byte(0x07)
	socksReplyAddressRejected = byte(0x08)
	socksReplyHostUnreachable = byte(0x04)
)

func negotiateSOCKS5(reader *bufio.Reader, client io.Writer) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 0x05 || header[1] == 0 {
		return errors.New("invalid SOCKS5 greeting")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return err
	}
	for _, method := range methods {
		if method == 0x00 {
			_, err := client.Write([]byte{0x05, 0x00})
			return err
		}
	}
	_, _ = client.Write([]byte{0x05, 0xff})
	return errors.New("SOCKS5 client does not support unauthenticated access")
}

func readSOCKS5Connect(reader *bufio.Reader, client io.Writer) (string, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return "", err
	}
	if header[0] != 0x05 {
		_ = writeSOCKS5Reply(client, socksReplyGeneralFailure, nil)
		return "", errors.New("invalid SOCKS5 request version")
	}
	if header[1] != 0x01 {
		_ = writeSOCKS5Reply(client, socksReplyCommandRejected, nil)
		return "", errors.New("unsupported SOCKS5 command")
	}

	var host string
	switch header[3] {
	case 0x01:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", err
		}
		host = net.IP(address).String()
	case 0x03:
		length, err := reader.ReadByte()
		if err != nil || length == 0 {
			return "", errors.New("invalid SOCKS5 domain")
		}
		address := make([]byte, int(length))
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", err
		}
		host = string(address)
	case 0x04:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", err
		}
		host = net.IP(address).String()
	default:
		_ = writeSOCKS5Reply(client, socksReplyAddressRejected, nil)
		return "", errors.New("unsupported SOCKS5 address type")
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))), nil
}

func writeSOCKS5Reply(client io.Writer, reply byte, bound net.Addr) error {
	ip := net.IPv4zero
	port := 0
	if tcp, ok := bound.(*net.TCPAddr); ok {
		ip = tcp.IP
		port = tcp.Port
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		response := []byte{0x05, reply, 0x00, 0x01, ipv4[0], ipv4[1], ipv4[2], ipv4[3], 0x00, 0x00}
		binary.BigEndian.PutUint16(response[8:], uint16(port))
		_, err := client.Write(response)
		return err
	}
	response := append([]byte{0x05, reply, 0x00, 0x04}, ip.To16()...)
	response = append(response, 0x00, 0x00)
	binary.BigEndian.PutUint16(response[len(response)-2:], uint16(port))
	_, err := client.Write(response)
	return err
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
