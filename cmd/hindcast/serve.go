package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/hindcast/hindcast/internal/alert"
	"github.com/hindcast/hindcast/internal/config"
	"github.com/hindcast/hindcast/internal/server"
)

func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("c", "hindcast.yaml", "config file")
	addr := fs.String("addr", ":9095", "listen address")
	traceDir := fs.String("traces", "hindcast-traces", "directory for traces and reports")
	tokenEnv := fs.String("token-env", "HINDCAST_WEBHOOK_TOKEN", "environment variable holding the bearer token Alertmanager must send")
	workers := fs.Int("workers", 2, "concurrent investigations")
	cooldown := fs.Duration("cooldown", time.Hour, "ignore repeat deliveries for an alert group for this long after investigating it")
	verbose := fs.Bool("v", false, "show MCP server stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *traceDir == "" {
		return errors.New("serve needs -traces: reports are written there")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	token := os.Getenv(*tokenEnv)
	if token == "" {
		log.Warn("no webhook token set; anyone who can reach the listen address can start investigations", "env", *tokenEnv)
	}

	l, err := openLive(ctx, cfg, *traceDir, serverLog(*verbose), os.Stderr)
	if err != nil {
		return err
	}
	defer l.Close()

	srv := server.New(server.Config{
		Investigate: func(ctx context.Context, p alert.Payload) error {
			rep, err := l.investigate(ctx, p)
			if rep != nil {
				log.Info("report", "alert", rep.Alert, "root_cause", rep.RootCause, "confidence", rep.Confidence)
			}
			return err
		},
		Token:    token,
		Workers:  *workers,
		Cooldown: *cooldown,
		Log:      log,
	})

	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	workersDone := make(chan struct{})
	go func() {
		srv.Run(ctx)
		close(workersDone)
	}()
	listenErr := make(chan error, 1)
	go func() { listenErr <- httpSrv.ListenAndServe() }()
	log.Info("listening", "addr", *addr, "endpoint", "POST /alerts")

	select {
	case err := <-listenErr:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "err", err)
	}
	<-workersDone
	return nil
}
