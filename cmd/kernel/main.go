package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"omni-bot/internal/envload"
	"omni-bot/internal/kernel"
	"omni-bot/internal/webui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "omni-bot kernel: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "serve":
			return runServe(ctx, args[1:], stdout, stderr, webui.Handler(), nil)
		case "migrate-legacy":
			return runMigrateLegacy(ctx, args[1:], stdout, stderr)
		case "help", "-h", "--help":
			printUsage(stdout)
			return nil
		default:
			if !strings.HasPrefix(args[0], "-") {
				return fmt.Errorf("unknown subcommand %q (supported: serve, migrate-legacy)", args[0])
			}
		}
	}
	return runServe(ctx, args, stdout, stderr, webui.Handler(), nil)
}

func runServe(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	webuiHandler http.Handler,
	onReady func(addr string),
) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)

	addr := fs.String("addr", ":8080", "HTTP/WebSocket listen address")
	dataDir := fs.String("data", "./data", "Data directory for SQLite WAL database, blobs, and memes")
	sandboxDir := fs.String("sandbox", "", "Optional isolated directory for default-off Bash/file tools")
	envFile := fs.String("env", "", "Optional .env.local path to load non-destructively")
	logLevel := fs.String("log-level", "info", "Log level (debug, info, warn, error)")
	botName := fs.String("bot-name", "bk101", "Bot display nickname in group timelines")
	gateThreshold := fs.Float64("gate-threshold", 0.50, "Decision probability threshold for replying in group chats")
	debounce := fs.Duration("debounce", 1500*time.Millisecond, "Debounce window for coalescing rapid group messages")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if strings.TrimSpace(*envFile) != "" {
		if err := envload.LoadFile(*envFile); err != nil {
			return fmt.Errorf("load env %q: %w", *envFile, err)
		}
	} else {
		if err := envload.LoadIfExists(".env.local"); err != nil {
			return fmt.Errorf("load .env.local: %w", err)
		}
	}

	logger := newLogger(stderr, *logLevel)
	slog.SetDefault(logger)

	rt, err := kernel.NewRuntime(ctx, kernel.RuntimeOptions{
		DataDir:        *dataDir,
		SandboxDir:     *sandboxDir,
		BotName:        *botName,
		GateThreshold:  *gateThreshold,
		DebounceWindow: *debounce,
		Logger:         logger,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		_ = rt.Close(context.Background())
		return fmt.Errorf("listen %q: %w", *addr, err)
	}

	srv := &http.Server{
		Handler:           rt.Handler(webuiHandler),
		ReadHeaderTimeout: 10 * time.Second,
	}

	boundAddr := ln.Addr().String()
	logger.Info("omni-bot kernel listening",
		slog.String("addr", boundAddr),
		slog.String("data_dir", *dataDir),
	)
	fmt.Fprintf(stdout, "omni-bot kernel listening on http://%s\n", boundAddr)
	if onReady != nil {
		onReady(boundAddr)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	var serveErr error
	select {
	case <-ctx.Done():
		logger.Info("shutting down omni-bot kernel")
	case serveErr = <-errCh:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	httpCloseErr := srv.Shutdown(shutdownCtx)
	if serveErr == nil {
		serveErr = <-errCh
	}
	rtCloseErr := rt.Close(shutdownCtx)
	return errors.Join(serveErr, httpCloseErr, rtCloseErr)
}

func newLogger(w io.Writer, levelStr string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl}))
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  kernel [serve] [--addr :8080] [--data ./data] [--sandbox DIR] [--env .env.local] [--log-level info]")
	fmt.Fprintln(w, "  kernel migrate-legacy --from <legacy_dir> [--data ./data] [--channel napcat] [--json]")
}
