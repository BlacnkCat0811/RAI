package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rai/server/internal/admin"
	"rai/server/internal/config"
	"rai/server/internal/httpapi"
	"rai/server/internal/jobs"
	"rai/server/internal/storage/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		// Do not print raw database errors, DSNs or health payloads.
		log.Error("command_failed", "reason", err.Error())
		os.Exit(1)
	}
}
func run(log *slog.Logger) error {
	if len(os.Args) != 2 {
		return errors.New("usage: rai api|worker|migrate|migrate-down|migrate-status|init|probe")
	}
	mode := os.Args[1]
	if mode == "probe" {
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/health")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return errors.New("not ready")
		}
		return nil
	}
	if mode != "api" && mode != "worker" && mode != "migrate" && mode != "migrate-down" && mode != "migrate-status" && mode != "init" {
		return errors.New("unknown command")
	}
	url, err := config.DatabaseURL()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
	if mode == "api" || mode == "worker" {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = store.Pool.Ping(pingCtx)
		cancel()
		if err != nil {
			return errors.New("database connection failed")
		}
	}
	if mode == "init" {
		result, err := admin.Init(ctx, store.Pool)
		if err != nil {
			return errors.New("dataset initialization failed")
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if mode == "worker" {
		return jobs.Run(ctx, store, log)
	}
	addr, err := config.ListenAddr()
	if err != nil {
		return err
	}
	server := &http.Server{Addr: addr, Handler: httpapi.New(store, log), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.New("HTTP listen failed")
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
