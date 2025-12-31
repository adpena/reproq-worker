package web

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"reproq-worker/internal/events"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	pool    *pgxpool.Pool
	addr    string
	token   string
	secret  string
	limiter *authLimiter
	allow   *CIDRAllowlist
	tls     *tls.Config
	events  *events.Broker
}

func NewServer(pool *pgxpool.Pool, addr string, token string, secret string, authLimit int, authWindow time.Duration, authMaxEntries int, allowlist *CIDRAllowlist, tlsConfig *tls.Config, broker *events.Broker) *Server {
	return &Server{
		pool:    pool,
		addr:    addr,
		token:   token,
		secret:  secret,
		limiter: newAuthLimiter(authLimit, authWindow, authMaxEntries),
		allow:   allowlist,
		tls:     tlsConfig,
		events:  broker,
	}
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !s.authorize(w, r) {
			return
		}
		// Ping DB as a basic readiness check
		if err := s.pool.Ping(r.Context()); err != nil {
			slog.Warn("Health check failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unhealthy"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Prometheus metrics
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !s.authorize(w, r) {
			return
		}
		promhttp.Handler().ServeHTTP(w, r)
	})

	// Server-sent events
	mux.HandleFunc("/events", s.handleEvents)

	server := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	if s.tls != nil {
		server.TLSConfig = s.tls
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("Metrics server shutdown error", "error", err)
		}
	}()

	if s.tls != nil {
		return server.ListenAndServeTLS("", "")
	}
	return server.ListenAndServe()
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.authorize(w, r) {
		return
	}
	if s.events == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("events not configured"))
		return
	}
	filter, err := parseEventFilter(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("streaming unsupported"))
		return
	}

	ch, cancel, snapshot := s.events.Subscribe()
	defer cancel()
	for _, event := range snapshot {
		if !filter.Matches(event) {
			continue
		}
		if err := writeEvent(w, event); err != nil {
			return
		}
		flusher.Flush()
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-ch:
			if !filter.Matches(event) {
				continue
			}
			if err := writeEvent(w, event); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, event events.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	host := remoteHost(r.RemoteAddr)
	if s.allow != nil && !s.allow.Allows(host) {
		limited := false
		if s.limiter != nil && !s.limiter.allow(host, time.Now()) {
			limited = true
		}
		slog.Warn(
			"Denied request",
			"path", r.URL.Path,
			"method", r.Method,
			"remote_addr", r.RemoteAddr,
			"remote_host", host,
			"reason", "allowlist",
			"rate_limited", limited,
		)
		if limited {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
		} else {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("forbidden"))
		}
		return false
	}
	if s.token == "" && s.secret == "" {
		return true
	}
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		token := strings.TrimSpace(authHeader[len("bearer "):])
		if s.token != "" && token == s.token {
			return true
		}
		if s.secret != "" && verifyTUIToken(token, s.secret) {
			return true
		}
	}
	if s.token != "" {
		limited := false
		if s.limiter != nil && !s.limiter.allow(host, time.Now()) {
			limited = true
		}
		slog.Warn(
			"Unauthorized request",
			"path", r.URL.Path,
			"method", r.Method,
			"remote_addr", r.RemoteAddr,
			"remote_host", host,
			"rate_limited", limited,
		)
		if limited {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
		}
		return false
	}
	return true
}

func remoteHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
