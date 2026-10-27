package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
		log.Error("command_failed", "reason", "configuration_or_storage_error")
		os.Exit(1)
	}
}
func run(log *slog.Logger) error {
	if len(os.Args) != 2 {
		return errors.New("usage: rai api|worker|migrate|init|probe")
	}
	mode := os.Args[1]
	if mode == "probe" {
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/readyz")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return errors.New("not ready")
		}
		return nil
	}
	if mode != "api" && mode != "worker" && mode != "migrate" && mode != "init" {
		return errors.New("unknown command")
	}
	url, err := config.DatabaseURL()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if mode == "migrate" {
		return admin.Migrate(ctx, url)
	}
	maxConns := int32(8)
	if mode == "worker" {
		maxConns = 4
	}
	store, err := postgres.Open(ctx, url, maxConns)
	if err != nil {
		return err
	}
	defer store.Pool.Close()
	if mode == "init" {
		result, err := admin.Init(ctx, store.Pool)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if mode == "worker" {
		return jobs.Run(ctx, store, log)
	}
	addr := os.Getenv("RAI_LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	server := &http.Server{Addr: addr, Handler: httpapi.New(store, log), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	log.Info("api_started", "address", addr)
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
