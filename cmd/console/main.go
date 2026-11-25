// Command console runs the gateway console: a web UI and API for managing
// llm-gateway API keys and watching spend and traffic.
//
//	console                  run the server (configured by environment)
//	console hash-password    read a password on stdin, print its hash for the users file
//	console -version         print the version
//
// Environment:
//
//	CONSOLE_LISTEN           public address for the UI and API (default :8090)
//	CONSOLE_INTERNAL_LISTEN  address for /internal/keys, for gateways only (default :8091)
//	CONSOLE_DATA_DIR         where keys and the audit log are stored (default ./data)
//	CONSOLE_USERS_FILE       JSON users file (default ./users.json)
//	CONSOLE_SESSION_SECRET   32+ characters, signs session cookies
//	CONSOLE_SYNC_TOKEN       32+ characters, shared with gateways
//	PROMETHEUS_URL           Prometheus that scrapes the gateways (optional)
//	CONSOLE_INSECURE_COOKIES set to "true" only for http://localhost
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sillykea/llm-gateway-console/internal/auth"
	"github.com/sillykea/llm-gateway-console/internal/prom"
	"github.com/sillykea/llm-gateway-console/internal/server"
	"github.com/sillykea/llm-gateway-console/internal/store"
	"github.com/sillykea/llm-gateway-console/web"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if flag.Arg(0) == "hash-password" {
		if err := hashPassword(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("version", version)
	if err := run(log); err != nil {
		log.Error("console stopped", "error", err)
		os.Exit(1)
	}
}

func hashPassword() error {
	fmt.Fprint(os.Stderr, "Password (at least 12 characters): ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	h, err := auth.HashPassword(strings.TrimRight(line, "\r\n"))
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func run(log *slog.Logger) error {
	users, err := auth.LoadUsers(env("CONSOLE_USERS_FILE", "users.json"))
	if err != nil {
		return err
	}
	insecure := os.Getenv("CONSOLE_INSECURE_COOKIES") == "true"
	if insecure {
		log.Warn("session cookies are not Secure: use this only on http://localhost")
	}
	sessions, err := auth.NewSessions(os.Getenv("CONSOLE_SESSION_SECRET"), !insecure)
	if err != nil {
		return fmt.Errorf("CONSOLE_SESSION_SECRET: %w", err)
	}
	st, err := store.Open(env("CONSOLE_DATA_DIR", "data"))
	if err != nil {
		return err
	}

	var metrics server.Metrics
	if u := os.Getenv("PROMETHEUS_URL"); u != "" {
		c, err := prom.New(u)
		if err != nil {
			return err
		}
		metrics = c
	} else {
		log.Warn("PROMETHEUS_URL is not set: the overview page will be empty")
	}

	ui := web.UI()
	if ui == nil {
		log.Warn("web UI is not built into this binary: run `make web` before `go build`")
	}
	srv, err := server.New(server.Options{
		Users: users, Sessions: sessions, Store: st, Metrics: metrics,
		SyncToken: os.Getenv("CONSOLE_SYNC_TOKEN"), UI: ui, Logger: log,
	})
	if err != nil {
		return err
	}

	public := &http.Server{
		Addr: env("CONSOLE_LISTEN", ":8090"), Handler: srv.PublicHandler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second,
	}
	internal := &http.Server{
		Addr: env("CONSOLE_INTERNAL_LISTEN", ":8091"), Handler: srv.InternalHandler(),
		ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	for _, s := range []*http.Server{public, internal} {
		go func() {
			log.Info("listening", "addr", s.Addr)
			if err := s.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return errors.Join(public.Shutdown(shutdownCtx), internal.Shutdown(shutdownCtx))
}
