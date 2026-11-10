package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
	_ "time/tzdata" // scratch image has no OS timezone database; freeze calendar support into the binary.

	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/config"
	"rai/server/internal/httpapi"
	"rai/server/internal/jobs"
	"rai/server/internal/storage/postgres"
	"rai/server/migrations"
)

var sourceCommit = "unrecorded"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		// Do not print raw database errors, DSNs or health payloads.
		log.Error("command_failed", "reason", err.Error())
		os.Exit(1)
	}
}
func run(log *slog.Logger) error {
	if len(os.Args) < 2 {
		return errors.New("usage: rai api|worker|migrate|migrate-down|migrate-status|init|probe")
	}
	mode := os.Args[1]
	if mode == "resources" {
		return config.ContainerResources(os.Stdout)
	}
	if mode == "version" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"source_commit": sourceCommit, "go": runtime.Version(), "schema": migrations.LatestVersion(), "sync_protocol": 3, "openapi": "1.13.3", "analysis": []string{analysis.WorkoutAlgorithm + "/" + analysis.WorkoutVersion, analysis.StateAlgorithm + "/" + analysis.StateVersion, analysis.DependencyAlgorithm + "/" + analysis.Version}, "training": "training_plan/v1", "coach_parameters": "coach_parameters/v1", "feedback": "training_feedback/v1", "training_merge": "training_merge/v2"})
	}
	if mode == "probe" {
		if len(os.Args) == 2 {
			return config.Probe(false)
		}
		if len(os.Args) == 3 && os.Args[2] == "--ready" {
			return config.Probe(true)
		}
		return errors.New("usage: rai probe [--ready]")
	}
	if mode != "admin" && mode != "ops" && mode != "worker-ready" && mode != "analysis" && mode != "api" && mode != "worker" && mode != "migrate" && mode != "migrate-down" && mode != "migrate-status" && mode != "init" {
		return errors.New("unknown command")
	}
	url, err := config.DatabaseURL()
	if err != nil {
		return err
	}
	if mode == "api" || mode == "worker" {
		if err = config.ApplyRateBudgets(); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if mode == "admin" {
		return admin.AdminCommand(ctx, url, os.Args[2:], os.Stdout)
	}
	if mode == "ops" {
		return admin.Operations(ctx, url, os.Args[2:], os.Stdout)
	}
	if mode == "analysis" {
		return admin.AnalysisCommand(ctx, url, os.Args[2:], os.Stdout)
	}
	if mode == "init" {
		return admin.InitCommand(ctx, url, os.Args[2:], os.Stdout)
	}
	if len(os.Args) != 2 {
		return errors.New("unexpected command arguments")
	}
	if mode == "migrate" {
		if err := admin.Migrate(ctx, url); err != nil {
			return errors.New("migration failed")
		}
		return nil
	}
	if mode == "migrate-down" {
		if err := admin.MigrateDown(ctx, url); err != nil {
			if errors.Is(err, admin.ErrMigrationIrreversible) {
				return err
			}
			return errors.New("migration rollback failed")
		}
		return nil
	}
	if mode == "migrate-status" {
		status, err := admin.MigrationStatus(ctx, url)
		if err != nil {
			return errors.New("migration status failed")
		}
		if err := json.NewEncoder(os.Stdout).Encode(status); err != nil {
			return errors.New("migration status output failed")
		}
		return nil
	}
	maxConns := int32(8)
	if mode == "worker" {
		maxConns = 4
	}
	store, err := postgres.Open(ctx, url, maxConns)
	if err != nil {
		return errors.New("invalid database connection configuration")
	}
	defer store.Pool.Close()
	if os.Getenv("RAI_ENFORCE_RUNTIME_ROLE") == "1" {
		if err = store.RuntimeRoleSafe(ctx); err != nil {
			return errors.New("unsafe database runtime role")
		}
	}
	if mode == "worker-ready" {
		return store.Ready(ctx)
	}
	if mode == "api" || mode == "worker" {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = store.Pool.Ping(pingCtx)
		cancel()
		if err != nil {
			return errors.New("database connection failed")
		}
	}
	if mode == "worker" {
		if err = store.Ready(ctx); err != nil {
			return errors.New("worker schema not ready")
		}
		return jobs.Run(ctx, store, log)
	}
	addr, err := config.ListenAddr()
	if err != nil {
		return err
	}
	server := httpapi.NewHTTPServer(addr, httpapi.New(store, log))
	server.TLSConfig, err = config.TLS()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.New("HTTP listen failed")
	}
	if server.TLSConfig != nil {
		listener = tls.NewListener(listener, server.TLSConfig)
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	log.Info("api_started", "address", addr)
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP server failed")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return errors.New("HTTP shutdown timed out or failed")
		}
		return nil
	}
}
