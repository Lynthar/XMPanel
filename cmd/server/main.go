package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xmpanel/xmpanel/internal/api/router"
	"github.com/xmpanel/xmpanel/internal/config"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"

	"go.uber.org/zap"
)

func main() {
	resetAdmin := flag.Bool("reset-admin", false,
		"Reset the admin account to a fresh random password and exit. "+
			"Other accounts are kept; admin's MFA is cleared and sessions revoked.")
	flag.Parse()

	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("failed to load configuration", zap.Error(err))
	}

	// Initialize database
	db, err := store.NewDB(cfg.Database)
	if err != nil {
		logger.Fatal("failed to initialize database", zap.Error(err))
	}
	defer db.Close()

	// Run migrations
	if err := store.Migrate(db); err != nil {
		logger.Fatal("failed to run migrations", zap.Error(err))
	}

	// Create password hasher
	hasher := crypto.NewArgon2Hasher(
		cfg.Security.Password.Argon2Time,
		cfg.Security.Password.Argon2Memory,
		cfg.Security.Password.Argon2Threads,
	)

	if *resetAdmin {
		if _, err := store.ResetAdmin(db, hasher.Hash); err != nil {
			logger.Fatal("failed to reset admin", zap.Error(err))
		}
		// ResetAdmin already logged the new credentials. Exit so the operator
		// can capture them and start the service via systemd.
		os.Exit(0)
	}

	// Ensure initial admin exists
	initResult, err := store.EnsureInitialAdmin(db, hasher.Hash)
	if err != nil {
		logger.Fatal("failed to ensure initial admin", zap.Error(err))
	}
	if initResult.AdminCreated {
		logger.Warn("initial admin created - please change the password immediately",
			zap.String("username", initResult.AdminUsername))
	}

	// Initialize router
	r := router.New(cfg, db, logger)

	// Configure server
	srv := &http.Server{
		Addr:         cfg.Server.Address,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		logger.Info("starting server", zap.String("address", cfg.Server.Address))
		if cfg.Server.TLS.Enabled {
			if err := srv.ListenAndServeTLS(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile); err != nil && err != http.ErrServerClosed {
				logger.Fatal("failed to start server", zap.Error(err))
			}
		} else {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Fatal("failed to start server", zap.Error(err))
			}
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("server forced to shutdown", zap.Error(err))
	}

	logger.Info("server stopped")
}
