package dataplane

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	defaultReadHeaderTimeout          = 5 * time.Second
	defaultIdleTimeout                = 90 * time.Second
	defaultMaxHeaderBytes             = 1 << 20
	defaultMaxConcurrentRequests      = 128
	defaultRequestBodyReadIdleTimeout = 30 * time.Second
)

// ServerConfig contains listener and HTTP server resource limits. WriteTimeout
// is intentionally omitted because it would break long-lived streamed bodies.
type ServerConfig struct {
	Address                    string
	ReadHeaderTimeout          time.Duration
	IdleTimeout                time.Duration
	MaxHeaderBytes             int
	MaxConcurrentRequests      int
	RequestBodyReadIdleTimeout time.Duration
}

// Validate rejects invalid resource-limit values. Zero values use defaults.
func (cfg ServerConfig) Validate() error {
	if cfg.MaxConcurrentRequests < 0 {
		return errors.New("max concurrent requests cannot be negative")
	}
	if cfg.RequestBodyReadIdleTimeout < 0 {
		return errors.New("request body read idle timeout cannot be negative")
	}
	return nil
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
	if err := cfg.Validate(); err != nil {
		return nil, err
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
	if cfg.MaxConcurrentRequests == 0 {
		cfg.MaxConcurrentRequests = defaultMaxConcurrentRequests
	}
	if cfg.RequestBodyReadIdleTimeout == 0 {
		cfg.RequestBodyReadIdleTimeout = defaultRequestBodyReadIdleTimeout
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
		Handler:           newResourceLimitHandler(s.handler, s.cfg.MaxConcurrentRequests, s.cfg.RequestBodyReadIdleTimeout),
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

type resourceLimitHandler struct {
	handler         http.Handler
	active          chan struct{}
	bodyReadTimeout time.Duration
}

func newResourceLimitHandler(handler http.Handler, maxConcurrent int, bodyReadTimeout time.Duration) http.Handler {
	return &resourceLimitHandler{
		handler:         handler,
		active:          make(chan struct{}, maxConcurrent),
		bodyReadTimeout: bodyReadTimeout,
	}
}

func (h *resourceLimitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body *idleTimedRequestBody
	if h.bodyReadTimeout > 0 && r.Body != nil && r.Body != http.NoBody {
		body = &idleTimedRequestBody{ReadCloser: r.Body, controller: http.NewResponseController(w), timeout: h.bodyReadTimeout}
		r.Body = body
	}
	select {
	case h.active <- struct{}{}:
		defer func() {
			if body != nil {
				body.finishHandler(w)
			}
			<-h.active
		}()
	default:
		if body != nil {
			body.reject(w)
		} else if r.ProtoMajor == 1 {
			w.Header().Set("Connection", "close")
		}
		http.Error(w, "server busy", http.StatusServiceUnavailable)
		return
	}
	if body == nil {
		h.handler.ServeHTTP(w, r)
		return
	}
	wrappedWriter := &requestBodyDeadlineWriter{ResponseWriter: w, body: body}
	if _, ok := w.(http.Hijacker); ok {
		h.handler.ServeHTTP(&hijackableRequestBodyDeadlineWriter{requestBodyDeadlineWriter: wrappedWriter}, r)
		return
	}
	h.handler.ServeHTTP(wrappedWriter, r)
}

type idleTimedRequestBody struct {
	io.ReadCloser
	controller  *http.ResponseController
	timeout     time.Duration
	mu          sync.Mutex
	sawEOF      bool
	finished    bool
	activeReads int
	hijacking   bool
	hijacked    bool
}

func (body *idleTimedRequestBody) Read(p []byte) (int, error) {
	body.mu.Lock()
	if body.finished || body.hijacking || body.hijacked {
		body.mu.Unlock()
		return 0, http.ErrBodyReadAfterClose
	}
	if err := body.controller.SetReadDeadline(time.Now().Add(body.timeout)); err != nil {
		body.mu.Unlock()
		return 0, err
	}
	body.activeReads++
	body.mu.Unlock()
	n, err := body.ReadCloser.Read(p)
	body.mu.Lock()
	body.activeReads--
	if err == io.EOF {
		body.sawEOF = true
	}
	if !body.hijacking && !body.hijacked {
		switch {
		case body.finished && !body.sawEOF:
			_ = body.controller.SetReadDeadline(time.Now())
		case body.activeReads > 0:
			_ = body.controller.SetReadDeadline(time.Now().Add(body.timeout))
		default:
			body.clearDeadline()
		}
	}
	body.mu.Unlock()
	return n, err
}

func (body *idleTimedRequestBody) Close() error {
	body.mu.Lock()
	var deadlineErr error
	if body.hijacking || body.hijacked {
		body.mu.Unlock()
		return nil
	}
	if !body.finished {
		deadlineErr = body.controller.SetReadDeadline(time.Now().Add(body.timeout))
	}
	body.mu.Unlock()
	closeErr := body.ReadCloser.Close()
	body.mu.Lock()
	if !body.finished && !body.hijacking && !body.hijacked {
		body.clearDeadline()
	}
	body.mu.Unlock()
	return errors.Join(deadlineErr, closeErr)
}

func (body *idleTimedRequestBody) reject(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	body.mu.Lock()
	if body.hijacking || body.hijacked {
		body.mu.Unlock()
		return
	}
	body.finished = true
	_ = body.controller.SetReadDeadline(time.Now())
	body.mu.Unlock()
}

func (body *idleTimedRequestBody) finishHandler(w http.ResponseWriter) {
	body.mu.Lock()
	if body.hijacking || body.hijacked {
		body.mu.Unlock()
		return
	}
	sawEOF := body.sawEOF
	if sawEOF {
		body.clearDeadline()
		body.finished = true
		body.mu.Unlock()
		return
	}
	w.Header().Set("Connection", "close")
	body.finished = true
	_ = body.controller.SetReadDeadline(time.Now())
	body.mu.Unlock()
}

func (body *idleTimedRequestBody) clearDeadline() {
	_ = body.controller.SetReadDeadline(time.Time{})
}

type requestBodyDeadlineWriter struct {
	http.ResponseWriter
	body *idleTimedRequestBody
}

func (w *requestBodyDeadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *requestBodyDeadlineWriter) WriteHeader(status int) {
	if status >= http.StatusOK || status == http.StatusSwitchingProtocols {
		w.body.finishHandler(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestBodyDeadlineWriter) Write(p []byte) (int, error) {
	w.body.finishHandler(w.ResponseWriter)
	return w.ResponseWriter.Write(p)
}

func (w *requestBodyDeadlineWriter) Flush() {
	w.body.finishHandler(w.ResponseWriter)
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

type hijackableRequestBodyDeadlineWriter struct {
	*requestBodyDeadlineWriter
}

func (w *hijackableRequestBodyDeadlineWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if !w.body.beginHijack() {
		return nil, nil, http.ErrHijacked
	}
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	w.body.finishHijack(err == nil)
	if err != nil {
		return nil, nil, err
	}
	return conn, rw, nil
}

func (body *idleTimedRequestBody) beginHijack() bool {
	body.mu.Lock()
	defer body.mu.Unlock()
	if body.hijacking || body.hijacked {
		return false
	}
	body.hijacking = true
	return true
}

func (body *idleTimedRequestBody) finishHijack(succeeded bool) {
	body.mu.Lock()
	body.hijacking = false
	if succeeded {
		body.hijacked = true
		body.clearDeadline()
		body.mu.Unlock()
		return
	}
	if body.hijacked {
		body.mu.Unlock()
		return
	}
	switch {
	case body.finished && !body.sawEOF:
		_ = body.controller.SetReadDeadline(time.Now())
	case body.activeReads > 0:
		_ = body.controller.SetReadDeadline(time.Now().Add(body.timeout))
	default:
		body.clearDeadline()
	}
	body.mu.Unlock()
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
