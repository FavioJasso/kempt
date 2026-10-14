package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
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
			"resolves (--dsn, $KEMPT_DSN, $DATABASE_PUBLIC_URL, $DATABASE_URL). KEMPT_DEMO=1 turns on\n" +
			"the public-demo limits: 5 MB / 50k rows per upload, 10 requests per minute per IP, and\n" +
			"hourly cleanup of demo schemas.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if addr == "" {
				addr = ":8080"
				if p := os.Getenv("PORT"); p != "" {
					addr = ":" + p
				}
			}
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			cfg := server.Config{
				Addr:    addr,
				Demo:    envTrue(os.Getenv("KEMPT_DEMO")),
				Web:     kempt.Web(),
				Samples: kempt.Samples(),
				Version: version,
				Logger:  log,
				Secret:  []byte(os.Getenv("KEMPT_SECRET")),
				// Railway (and any proxy set with KEMPT_TRUST_PROXY=1) puts the
				// client address in X-Forwarded-For.
				TrustProxy: envTrue(os.Getenv("KEMPT_TRUST_PROXY")) || os.Getenv("RAILWAY_PROJECT_ID") != "",
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
