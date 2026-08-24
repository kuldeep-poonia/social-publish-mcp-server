// Package main is the entry point for the Social Publishing & Analytics MCP Server.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// AppVersion represents the current version of the server.
const AppVersion = "0.1.0"

func main() {
	log.Printf("Starting Social Publishing & Analytics MCP Server v%s", AppVersion)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx); err != nil {
		log.Fatalf("Server startup failed: %v", err)
	}
}

func run(ctx context.Context) error {
	log.Println("Server initialized in development mode. Waiting for shutdown signal...")

	<-ctx.Done()
	log.Println("Graceful shutdown initiated...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	// Handle cleanup tasks here
	<-shutdownCtx.Done()
	fmt.Println("Server terminated cleanly.")
	return nil
}
