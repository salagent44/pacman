// Command pacman is a single-binary file drop and its own client.
//
//	pacman serve      runs the server: upload with PUT/POST, list, download, delete,
//	                  every route behind a shared token.
//	pacman <cmd>      talks to a server: ls, get, put, install, update, rm, login.
//
// The same static binary is uploaded to the server, so a fresh machine only
// needs curl to bootstrap and can then keep itself and everything else current
// with `pacman update`.
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

// version is stamped by the Makefile via -ldflags "-X main.version=...".
var version = "dev"

const usageText = `pacman - a tiny binary drop with token auth

Server:
  pacman serve [-addr :8080] [-dir ./data] [-token T]
        env: PACMAN_ADDR, PACMAN_DIR, PACMAN_TOKEN (preferred for the token)

Client:
  pacman login URL TOKEN          save the server and token to ~/.config/pacman/config
  pacman ls                       list files on the server
  pacman put FILE [NAME]          upload FILE (as NAME, default its basename)
  pacman get NAME [DEST]          download NAME to DEST (default ./NAME)
  pacman install [-dir D] NAME... download to ~/.local/bin, chmod +x, remember it
  pacman update [NAME...]         re-download installed files that changed on the server
  pacman rm NAME                  delete NAME from the server
  pacman version                  print the build version

  env: PACMAN_URL and PACMAN_TOKEN override the config file; PACMAN_BIN sets the
  install dir. Installed files are tracked in ~/.local/share/pacman/installed.json.
`

func usage() { fmt.Fprint(os.Stderr, usageText) }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "--help", "help":
		usage()
		return
	case "serve":
		serve(os.Args[2:])
		return
	}
	if err := runClient(os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, errUsage) {
			usage()
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "pacman:", err)
		os.Exit(1)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("pacman serve", flag.ExitOnError)
	addr := fs.String("addr", envOr("PACMAN_ADDR", ":8080"), "listen address (env PACMAN_ADDR)")
	dir := fs.String("dir", envOr("PACMAN_DIR", "./data"), "storage directory (env PACMAN_DIR)")
	token := fs.String("token", os.Getenv("PACMAN_TOKEN"), "auth token, required (env PACMAN_TOKEN, preferred so it stays out of ps)")
	fs.Parse(args)

	if *token == "" {
		fmt.Fprintln(os.Stderr, "pacman serve: a token is required (set PACMAN_TOKEN or pass -token)")
		os.Exit(2)
	}
	if err := os.MkdirAll(*dir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "pacman serve: create storage dir: %v\n", err)
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
		log.Printf("pacman %s listening on %s, storing in %s", version, *addr, *dir)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("pacman serve: %v", err)
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
