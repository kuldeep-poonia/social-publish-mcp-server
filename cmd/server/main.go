// Package main is the entry point for the Social Publishing & Analytics MCP Server.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/server"
)

// AppVersion represents the current version of the server.
const AppVersion = "0.1.0"

func main() {
	log.Printf("Starting Social Publishing & Analytics MCP Server v%s", AppVersion)

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Fatal: configuration loading failed: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, cfg); err != nil {
		log.Fatalf("Server terminated with error: %v", err)
	}
}

func run(ctx context.Context, cfg *config.Config) error {
	// Initialize PostgreSQL connection pool
	db, err := sql.Open("pgx", cfg.PostgresDSN())
	if err != nil {
		return fmt.Errorf("failed opening database connection pool: %w", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		log.Printf("Warning: Postgres not ready at startup (%v). Running in decoupled mode.", err)
	} else {
		log.Println("PostgreSQL connection pool established and verified.")

		// Automated Migration-on-Startup Engine
		log.Println("Verifying database schema migrations...")
		applied, err := database.RunMigrations(ctx, db)
		if err != nil {
			return fmt.Errorf("fatal: database migration failed on startup: %w", err)
		}
		if applied > 0 {
			log.Printf("Database schema update completed: %d new migration(s) applied successfully.", applied)
		} else {
			log.Println("Database schema is up to date (0 pending migrations).")
		}
	}

	log.Printf("Twitter Integration Status: ClientID configured: %t, RedirectURI: %s", cfg.TwitterClientID != "", cfg.TwitterRedirectURI)

	repo := database.NewRepository(db, cfg.TokenEncryptionKey, nil)
	httpServer := server.NewHTTPServer(cfg, db, repo)

	serverErrChan := make(chan error, 1)
	go func() {
		log.Printf("MCP HTTP Server listening on %s:%d", cfg.ServerHost, cfg.ServerPort)
		if err := httpServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrChan <- err
		}
	}()

	select {
	case err := <-serverErrChan:
		return fmt.Errorf("HTTP server failure: %w", err)
	case <-ctx.Done():
		log.Println("Graceful shutdown signal received...")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}

	fmt.Println("Server terminated cleanly.")
	return nil
}
