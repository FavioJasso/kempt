// Package server implements `kempt serve`: the embedded web demo and its
// JSON API (inspect, load into a throwaway Postgres schema, read-only query).
package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FavioJasso/kempt/internal/dsn"
)

// Demo-mode limits (KEMPT_DEMO=1).
const (
	demoMaxUpload = 5 << 20
	demoMaxRows   = 50_000
	demoMaxCells  = 1_000_000 // rows x columns, checked before the grid is allocated
	demoRate      = 10        // requests per minute per IP
	demoAnalyses  = 2         // spreadsheets read at once
	schemaTTL     = time.Hour
	janitorEvery  = 10 * time.Minute

	defaultMaxUpload = 64 << 20
	defaultMaxCells  = 20_000_000
	defaultAnalyses  = 4

	// In demo mode, formulas without a cached value are evaluated only on
	// sheets with at most this many values: evaluating loads the whole sheet
	// into memory. Larger sheets read them as #CALC!.
	demoCalcLimit = 200_000
	// Queries in flight at once (each opens its own connection).
	maxQueries = 4
	// How long a request waits for an analysis or query slot.
	slotWait = 20 * time.Second

	gridRows    = 200 // rows of the original grid returned by /api/inspect
	gridCols    = 30
	previewRows = 50
	queryRows   = 100
)

// Config configures a Server.
type Config struct {
	Addr    string
	Demo    bool        // public-demo protections: upload/row caps, rate limit, janitor
	DSN     *dsn.Target // nil disables /api/load and /api/query
	Web     fs.FS       // index.html, app.js, style.css
	Samples fs.FS       // sample spreadsheets offered by /api/samples
	Version string
	Logger  *slog.Logger
	// TrustProxy makes the rate limiter key on X-Forwarded-For, which only a
	// trusted reverse proxy in front of the server can vouch for.
	TrustProxy bool
	// Secret derives per-schema passwords for query roles. Empty means a
	// random secret per process (schemas become unqueryable after restart).
	Secret []byte
}

// Server is the HTTP server for kempt serve.
type Server struct {
	cfg       Config
	log       *slog.Logger
	pool      *pgxpool.Pool
	limiter   *limiter
	maxUpload int64
	maxRows   int
	maxCells  int64
	calcLimit int
	analyses  chan struct{} // analysis slots
	queries   chan struct{} // query slots
	handler   http.Handler
}

// New builds a server. When cfg.DSN is a Postgres DSN it opens a connection
// pool lazily (connections are made on first use).
func New(cfg Config) (*Server, error) {
	s := &Server{cfg: cfg, log: cfg.Logger, maxUpload: defaultMaxUpload, maxCells: defaultMaxCells,
		analyses: make(chan struct{}, defaultAnalyses), queries: make(chan struct{}, maxQueries)}
	if s.log == nil {
		s.log = slog.Default()
	}
	if len(s.cfg.Secret) == 0 {
		s.cfg.Secret = make([]byte, 32)
		if _, err := rand.Read(s.cfg.Secret); err != nil {
			return nil, err
		}
	}
	if cfg.Demo {
		s.maxUpload, s.maxRows, s.maxCells, s.calcLimit = demoMaxUpload, demoMaxRows, demoMaxCells, demoCalcLimit
		s.analyses = make(chan struct{}, demoAnalyses)
		s.limiter = newLimiter(demoRate, time.Minute)
	}
	if cfg.DSN != nil {
		if cfg.DSN.Dialect != "postgres" {
			s.log.Warn("load and query need Postgres; disabling them", "dialect", cfg.DSN.Dialect)
		} else {
			pc, err := pgxpool.ParseConfig(cfg.DSN.DSN)
			if err != nil {
				return nil, err
			}
			pc.MaxConns = 5
			if s.pool, err = pgxpool.NewWithConfig(context.Background(), pc); err != nil {
				return nil, err
			}
		}
	}
	s.handler = s.routes()
	return s, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// Close releases the database pool.
func (s *Server) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// acquire takes a slot from sem, waiting up to slotWait. The caller must
// call the returned release.
func acquire(ctx context.Context, sem chan struct{}) (func(), error) {
	t := time.NewTimer(slotWait)
	defer t.Stop()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-t.C:
		return nil, &httpError{http.StatusServiceUnavailable, "the server is busy; try again in a moment"}
	case <-ctx.Done():
		return nil, &httpError{http.StatusServiceUnavailable, "the request was cancelled"}
	}
}

// Run serves until ctx is cancelled, then shuts down gracefully. When
// loading is enabled it also runs the janitor that drops loaded schemas
// after an hour.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	if s.pool != nil {
		go s.janitor(ctx)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	s.log.Info("kempt serve listening", "addr", ln.Addr().String(), "demo", s.cfg.Demo, "load", s.pool != nil)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdown)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/samples", s.handleSamples)
	mux.HandleFunc("POST /api/inspect", s.limited(s.handleInspect))
	mux.HandleFunc("POST /api/load", s.limited(s.handleLoad))
	mux.HandleFunc("POST /api/query", s.limited(s.handleQuery))
	if s.cfg.Web != nil {
		mux.Handle("GET /", http.FileServerFS(s.cfg.Web))
	}
	return securityHeaders(mux)
}

// limited applies the per-IP rate limit in demo mode.
func (s *Server) limited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.limiter != nil {
			if ok, wait := s.limiter.allow(clientIP(r, s.cfg.TrustProxy)); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
				writeError(w, http.StatusTooManyRequests, "rate limit: the public demo allows 10 requests per minute; try again shortly")
				return
			}
		}
		h(w, r)
	}
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"loadEnabled":    s.pool != nil,
		"demo":           s.cfg.Demo,
		"maxUploadBytes": s.maxUpload,
		"maxRows":        s.maxRows,
		"version":        s.cfg.Version,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
