package web

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	pool    *pgxpool.Pool
	addr    string
	token   string
	limiter *authLimiter
	allow   *CIDRAllowlist
	tls     *tls.Config
}

func NewServer(pool *pgxpool.Pool, addr string, token string, authLimit int, authWindow time.Duration, authMaxEntries int, allowlist *CIDRAllowlist, tlsConfig *tls.Config) *Server {
	return &Server{
		pool:    pool,
		addr:    addr,
		token:   token,
		limiter: newAuthLimiter(authLimit, authWindow, authMaxEntries),
		allow:   allowlist,
		tls:     tlsConfig,
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
			w.Write([]byte("unhealthy"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
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
		server.Shutdown(shutdownCtx)
	}()

	if s.tls != nil {
		return server.ListenAndServeTLS("", "")
	}
	return server.ListenAndServe()
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
			w.Write([]byte("rate limited"))
		} else {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("forbidden"))
		}
		return false
	}
	if s.token == "" {
		return true
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
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
			w.Write([]byte("rate limited"))
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("unauthorized"))
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
