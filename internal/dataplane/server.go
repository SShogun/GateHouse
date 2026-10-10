package dataplane

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultIdleTimeout       = 90 * time.Second
	defaultMaxHeaderBytes    = 1 << 20
)

// ServerConfig contains listener and HTTP server resource limits. WriteTimeout
// is intentionally omitted because it would break long-lived streamed bodies.
type ServerConfig struct {
	Address           string
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
}

// Server owns the listener and drains active HTTP requests on shutdown.
type Server struct {
	mu       sync.Mutex
	handler  http.Handler
	cfg      ServerConfig
	server   *http.Server
	listener net.Listener
	serveErr chan error
	started  bool
}

// NewServer validates and configures a server but does not bind its address.
func NewServer(handler http.Handler, cfg ServerConfig) (*Server, error) {
	if handler == nil {
		return nil, errors.New("HTTP handler is nil")
	}
	if cfg.Address == "" {
		return nil, errors.New("server address is required")
	}
	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	if cfg.MaxHeaderBytes <= 0 {
		cfg.MaxHeaderBytes = defaultMaxHeaderBytes
	}
	return &Server{handler: handler, cfg: cfg}, nil
}

// Start binds the configured listener and starts accepting HTTP requests.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("HTTP server already started")
	}
	listener, err := net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Address, err)
	}
	s.listener = listener
	s.server = &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
		IdleTimeout:       s.cfg.IdleTimeout,
		MaxHeaderBytes:    s.cfg.MaxHeaderBytes,
	}
	s.serveErr = make(chan error, 1)
	s.started = true
	go func(server *http.Server, listener net.Listener, result chan<- error) {
		result <- server.Serve(listener)
	}(s.server, listener, s.serveErr)
	return nil
}

// Addr returns the actual listener address after Start, including an assigned
// port when Address used port zero.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Wait returns when the server stops or ctx is canceled. A normal HTTP server
// shutdown is reported as nil.
func (s *Server) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("wait context is nil")
	}
	s.mu.Lock()
	started, result := s.started, s.serveErr
	s.mu.Unlock()
	if !started {
		return errors.New("HTTP server has not started")
	}
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		return nil
	}
}

// Shutdown stops accepting new requests and waits for active requests to
// finish within ctx. If the deadline expires, it forcibly closes connections.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shutdown context is nil")
	}
	s.mu.Lock()
	server, started := s.server, s.started
	s.mu.Unlock()
	if !started {
		return errors.New("HTTP server has not started")
	}
	err := server.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, server.Close())
	}
	if closer, ok := s.handler.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	return err
}
