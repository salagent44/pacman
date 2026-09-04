// Command pacman is a single-binary file drop: upload binaries with PUT/POST,
// list them, and download them over plain HTTP from any machine. Every
// endpoint except /healthz requires a shared token.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", envOr("PACMAN_ADDR", ":8080"), "listen address (env PACMAN_ADDR)")
	dir := flag.String("dir", envOr("PACMAN_DIR", "./data"), "storage directory (env PACMAN_DIR)")
	token := flag.String("token", os.Getenv("PACMAN_TOKEN"), "auth token, required (env PACMAN_TOKEN, preferred so it stays out of ps)")
	flag.Parse()

	if *token == "" {
		fmt.Fprintln(os.Stderr, "pacman: a token is required (set PACMAN_TOKEN or pass -token)")
		os.Exit(2)
	}
	if err := os.MkdirAll(*dir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "pacman: create storage dir: %v\n", err)
		os.Exit(1)
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           newServer(*dir, *token).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Printf("pacman listening on %s, storing in %s", *addr, *dir)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("pacman: %v", err)
		}
	case <-ctx.Done():
		log.Printf("pacman: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("pacman: shutdown: %v", err)
		}
	}
}
