package forwardproxy

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Server exposes the proxy gateway's HTTP/HTTPS and SOCKS5 protocols on one
// TCP address. SOCKS5 starts with version byte 0x05; every other connection is
// handed to net/http unchanged.
type Server struct {
	Addr              string
	Handler           *Proxy
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration

	mu         sync.Mutex
	httpServer *http.Server
}

func (s *Server) ListenAndServe() error {
	listener, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	return s.Serve(listener)
}

func (s *Server) Serve(listener net.Listener) error {
	multiplexed := newProtocolListener(listener, s.Handler.ServeSOCKS)
	server := &http.Server{
		Handler:           s.Handler,
		ReadHeaderTimeout: s.ReadHeaderTimeout,
		IdleTimeout:       s.IdleTimeout,
	}
	s.mu.Lock()
	s.httpServer = server
	s.mu.Unlock()
	err := server.Serve(multiplexed)
	if errors.Is(err, net.ErrClosed) {
		return http.ErrServerClosed
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	server := s.httpServer
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

type connectionKind uint8

const (
	connectionClassifying connectionKind = iota
	connectionHTTP
	connectionSOCKS
)

type protocolListener struct {
	base        net.Listener
	handleSOCKS func(net.Conn)
	httpConns   chan net.Conn
	acceptError chan error
	done        chan struct{}
	closeOnce   sync.Once
	mu          sync.Mutex
	connections map[*trackedConn]connectionKind
}

func newProtocolListener(base net.Listener, handleSOCKS func(net.Conn)) *protocolListener {
	listener := &protocolListener{
		base: base, handleSOCKS: handleSOCKS,
		httpConns: make(chan net.Conn), acceptError: make(chan error, 1),
		done: make(chan struct{}), connections: make(map[*trackedConn]connectionKind),
	}
	go listener.acceptLoop()
	return listener
}

func (l *protocolListener) acceptLoop() {
	for {
		connection, err := l.base.Accept()
		if err != nil {
			l.finish(err)
			return
		}
		tracked := &trackedConn{Conn: connection}
		tracked.onClose = func() {
			l.mu.Lock()
			delete(l.connections, tracked)
			l.mu.Unlock()
		}
		l.mu.Lock()
		l.connections[tracked] = connectionClassifying
		l.mu.Unlock()
		go l.classify(tracked)
	}
}

func (l *protocolListener) classify(connection *trackedConn) {
	_ = connection.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(connection)
	first, err := reader.Peek(1)
	_ = connection.SetReadDeadline(time.Time{})
	if err != nil {
		_ = connection.Close()
		return
	}
	buffered := &bufferedConn{Conn: connection, reader: reader}
	if first[0] == 0x05 {
		l.setKind(connection, connectionSOCKS)
		l.handleSOCKS(buffered)
		_ = connection.Close()
		return
	}
	l.setKind(connection, connectionHTTP)
	select {
	case l.httpConns <- buffered:
	case <-l.done:
		_ = connection.Close()
	}
}

func (l *protocolListener) setKind(connection *trackedConn, kind connectionKind) {
	l.mu.Lock()
	if _, exists := l.connections[connection]; exists {
		l.connections[connection] = kind
	}
	l.mu.Unlock()
}

func (l *protocolListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.httpConns:
		return connection, nil
	case err := <-l.acceptError:
		return nil, err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *protocolListener) Close() error {
	err := l.base.Close()
	l.finish(net.ErrClosed)
	l.mu.Lock()
	connections := make([]*trackedConn, 0, len(l.connections))
	for connection, kind := range l.connections {
		if kind != connectionHTTP {
			connections = append(connections, connection)
		}
	}
	l.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
	return err
}

func (l *protocolListener) Addr() net.Addr { return l.base.Addr() }

func (l *protocolListener) finish(err error) {
	l.closeOnce.Do(func() {
		select {
		case l.acceptError <- err:
		default:
		}
		close(l.done)
	})
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(buffer []byte) (int, error) { return c.reader.Read(buffer) }

type trackedConn struct {
	net.Conn
	onClose   func()
	closeOnce sync.Once
	err       error
}

func (c *trackedConn) Close() error {
	c.closeOnce.Do(func() {
		c.err = c.Conn.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.err
}
