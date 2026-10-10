// Package dataplane contains the static data-plane HTTP proxy used by M1.
package dataplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultResponseHeaderTimeout  = 30 * time.Second
	defaultMaxResponseHeaderBytes = 1 << 20
)

// Config describes the single static upstream used by the M1 data plane.
type Config struct {
	UpstreamURL            string
	ResponseHeaderTimeout  time.Duration
	MaxResponseHeaderBytes int64
}

// Proxy forwards all requests to one configured upstream without buffering
// response bodies. It is intentionally not a route or cluster abstraction.
type Proxy struct {
	upstream  *httputil.ReverseProxy
	transport *http.Transport
	logger    *slog.Logger
	active    atomic.Int64
}

type requestOutcomeKey struct{}

type requestOutcome struct {
	failure bool
}

// NewProxy creates a reverse proxy for a static HTTP or HTTPS upstream.
func NewProxy(cfg Config, logger *slog.Logger) (*Proxy, error) {
	if cfg.ResponseHeaderTimeout < 0 {
		return nil, errors.New("response header timeout cannot be negative")
	}
	if cfg.MaxResponseHeaderBytes < 0 {
		return nil, errors.New("max response header bytes cannot be negative")
	}
	if cfg.ResponseHeaderTimeout == 0 {
		cfg.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	}
	if cfg.MaxResponseHeaderBytes == 0 {
		cfg.MaxResponseHeaderBytes = defaultMaxResponseHeaderBytes
	}
	target, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}
	if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, errors.New("upstream URL must be an absolute http or https URL")
	}
	if target.User != nil || target.RawQuery != "" || target.Fragment != "" || (target.Path != "" && target.Path != "/") {
		return nil, errors.New("upstream URL must contain only a scheme and host")
	}
	if logger == nil {
		logger = slog.Default()
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = cfg.ResponseHeaderTimeout
	transport.MaxResponseHeaderBytes = cfg.MaxResponseHeaderBytes
	p := &Proxy{logger: logger, transport: transport}
	p.upstream = &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.SetXForwarded()
		},
		FlushInterval: -1,
		Transport:     transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if outcome, ok := r.Context().Value(requestOutcomeKey{}).(*requestOutcome); ok {
				outcome.failure = true
			}
			p.logger.Warn("upstream request failed", "method", r.Method, "error", err)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}
	return p, nil
}

// ActiveRequests reports the number of requests currently being proxied.
func (p *Proxy) ActiveRequests() int64 { return p.active.Load() }

// CloseIdleConnections releases pooled upstream connections after shutdown.
func (p *Proxy) CloseIdleConnections() { p.transport.CloseIdleConnections() }

// ServeHTTP proxies one request and records a bounded structured outcome.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.active.Add(1)
	defer p.active.Add(-1)
	started := time.Now()
	outcome := &requestOutcome{}
	r = r.WithContext(context.WithValue(r.Context(), requestOutcomeKey{}, outcome))
	recorder := &statusRecorder{ResponseWriter: w}
	defer func() {
		if recovered := recover(); recovered != nil {
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			p.logger.Warn("proxy request aborted",
				"method", r.Method,
				"status", status,
				"outcome", "failure",
				"error", fmt.Sprint(recovered),
				"duration", time.Since(started),
			)
			panic(recovered)
		}
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		loggedOutcome := "success"
		if outcome.failure {
			loggedOutcome = "failure"
		}
		p.logger.Info("proxy request complete",
			"method", r.Method,
			"status", status,
			"outcome", loggedOutcome,
			"duration", time.Since(started),
		)
	}()
	p.upstream.ServeHTTP(recorder, r)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		stripHopByHopHeaders(w.Header())
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func stripHopByHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, field := range strings.Split(value, ",") {
			if field = textproto.TrimString(field); field != "" {
				header.Del(field)
			}
		}
	}
	for _, field := range [...]string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		header.Del(field)
	}
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusRecorder) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
