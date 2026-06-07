package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vetka-backend-panel/internal/config"
	"vetka-backend-panel/internal/db"
	panelhttp "vetka-backend-panel/internal/http"
)

func main() {
	showHelp := flag.Bool("help", false, "show help")
	migrateOnly := flag.Bool("migrate-up", false, "apply database migrations and exit")
	flag.Parse()

	if *showHelp {
		fmt.Println("Vetka Backend Panel")
		flag.PrintDefaults()
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		logger.Error("apply migrations", "error", err)
		os.Exit(1)
	}
	if *migrateOnly {
		logger.Info("migrations applied")
		return
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           panelhttp.NewServer(cfg, pool, logger),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("starting server", "addr", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown server", "error", err)
		os.Exit(1)
	}
}
