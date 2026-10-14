package main

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/FavioJasso/kempt"
	"github.com/FavioJasso/kempt/internal/dsn"
	"github.com/FavioJasso/kempt/internal/server"
)

func serveCmd() *cobra.Command {
	var addr, dsnFlag string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the web demo and JSON API",
		Long: "Run the web demo and JSON API. Loading and querying are enabled when a Postgres DSN\n" +
			"resolves (--dsn, $KEMPT_DSN, $DATABASE_PUBLIC_URL, $DATABASE_URL); loaded schemas and\n" +
			"their query roles are dropped after an hour. KEMPT_DEMO=1 turns on the public-demo\n" +
			"limits: 5 MB, 50k rows and 1M cells per upload, two uploads read at a time, and 10\n" +
			"requests per minute per IP.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if addr == "" {
				addr = ":8080"
				if p := os.Getenv("PORT"); p != "" {
					addr = ":" + p
				}
			}
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			// On Railway the edge puts the client address in X-Real-IP; the
			// right-most X-Forwarded-For entry is Railway's own.
			onRailway := os.Getenv("RAILWAY_PROJECT_ID") != ""
			realIP := os.Getenv("KEMPT_CLIENT_IP_HEADER")
			if realIP == "" && onRailway {
				realIP = "X-Real-IP"
			}
			limit := server.MemoryLimit()
			switch {
			case os.Getenv("GOMEMLIMIT") != "":
				// The operator set Go's soft limit; size the server from it.
				if soft := debug.SetMemoryLimit(-1); soft < math.MaxInt64 {
					limit = soft / 8 * 10
				}
			case limit > 0:
				// Make the garbage collector work harder near the container's
				// limit instead of letting the kernel kill the process.
				debug.SetMemoryLimit(limit / 10 * 8)
			}
			if limit > 0 {
				log.Info("memory", "limit_mib", limit>>20, "go_soft_limit_mib", debug.SetMemoryLimit(-1)>>20)
			}
			cfg := server.Config{
				Addr:         addr,
				Demo:         envTrue(os.Getenv("KEMPT_DEMO")),
				Web:          kempt.Web(),
				Samples:      kempt.Samples(),
				Version:      version,
				Logger:       log,
				Secret:       []byte(os.Getenv("KEMPT_SECRET")),
				TrustProxy:   envTrue(os.Getenv("KEMPT_TRUST_PROXY")) || onRailway,
				RealIPHeader: realIP,
				MemoryLimit:  limit,
			}
			target, err := dsn.Resolve(dsnFlag, os.Getenv)
			switch {
			case err == nil:
				log.Info("database", "dsn", target.Describe())
				cfg.DSN = &target
			case errors.Is(err, dsn.ErrNoDSN):
				log.Info("no database DSN; loading and querying are disabled")
			default:
				return fmt.Errorf("database DSN: %w", err)
			}
			srv, err := server.New(cfg)
			if err != nil {
				return err
			}
			defer srv.Close()
			return srv.Run(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "listen address (default :$PORT, else :8080)")
	cmd.Flags().StringVar(&dsnFlag, "dsn", "", "Postgres DSN for /api/load and /api/query")
	return cmd
}

func envTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
