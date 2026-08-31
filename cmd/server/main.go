// Package main is the entry point for the Social Publishing & Analytics MCP Server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/server"
	"github.com/redis/go-redis/v9"
)

// Version and Commit hold the build-time application version and git commit SHA.
var (
	version = "1.0.0"
	commit  = "a4c74dc"
)

func main() {
	if version != "" {
		server.Version = version
	}
	if commit != "" {
		server.Commit = commit
	}
	log.Printf("Starting Social Publishing & Analytics MCP Server v%s (commit: %s)", server.Version, server.Commit)

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
	// Initialize PostgreSQL connection pool with pooler compatibility (disabling statement cache for Supabase/PgBouncer)
	connConfig, err := pgx.ParseConfig(cfg.PostgresDSN())
	if err != nil {
		return fmt.Errorf("failed parsing database connection string: %w", err)
	}
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	connConfig.StatementCacheCapacity = 0

	db := stdlib.OpenDB(*connConfig)
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

	// Startup Redis Verification
	if redisOpts, err := cfg.RedisOptions(); err != nil {
		log.Printf("[Redis Startup] ERROR constructing Redis options: %v", err)
	} else if redisOpts != nil {
		testRdb := redis.NewClient(redisOpts)
		rPingCtx, rPingCancel := context.WithTimeout(ctx, 3*time.Second)
		if rErr := testRdb.Ping(rPingCtx).Err(); rErr != nil {
			log.Printf("[Redis Startup] Warning: Redis ping failed (%v). Running in degraded mode.", rErr)
		} else {
			log.Printf("[Redis Startup] Redis connection established and verified via ping to %s (TLS=%t).", redisOpts.Addr, redisOpts.TLSConfig != nil)
		}
		_ = testRdb.Close()
		rPingCancel()
	} else {
		log.Println("[Redis Startup] No Redis configuration detected. Running in memory-only mode.")
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
