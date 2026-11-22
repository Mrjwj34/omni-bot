package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	if err := runMain(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "napcat-adapter: %v\n", err)
		os.Exit(1)
	}
}

func runMain(args []string) error {
	fs := flag.NewFlagSet("napcat-adapter", flag.ContinueOnError)

	var (
		kernelURL       = fs.String("kernel-url", "http://127.0.0.1:8080", "Kernel HTTP base URL (for /ws and /api/blobs)")
		kernelWSURL     = fs.String("kernel-ws-url", "", "Optional Kernel WS URL override (defaults to <kernel-url>/ws)")
		napcatWSURL     = fs.String("napcat-ws-url", "", "NapCat OneBot v11 WS URL (client mode; defaults to ws://127.0.0.1:3001 if -napcat-listen is unset)")
		napcatListen    = fs.String("napcat-listen", "", "Optional reverse WS listen address for NapCat (e.g. :3001)")
		napcatToken     = fs.String("napcat-token", "", "Bearer token for NapCat WS auth (falls back to NAPCAT_TOKEN env)")
		channelID       = fs.String("channel-id", defaultChannelID, "Channel ID registered with Kernel")
		ffmpegPath      = fs.String("ffmpeg", "ffmpeg", "Path to ffmpeg binary for SILK/AMR voice transcoding")
		maxForwardDepth = fs.Int("max-forward-depth", defaultMaxForwardDepth, "Max recursive depth for QQ forward messages")
		actionTimeout   = fs.Duration("action-timeout", defaultActionTimeout, "Per-action OneBot WS timeout")
		rpcTimeout      = fs.Duration("rpc-timeout", defaultKernelRPCTimeout, "Per-call Kernel JSON-RPC timeout")
		logLevel        = fs.String("log-level", "info", "Log level: debug, info, warn, error")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	wsURL := strings.TrimSpace(*napcatWSURL)
	listenAddr := strings.TrimSpace(*napcatListen)
	if wsURL == "" && listenAddr == "" {
		wsURL = "ws://127.0.0.1:3001"
	}

	token := *napcatToken
	if token == "" {
		token = os.Getenv("NAPCAT_TOKEN")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLogLevel(*logLevel),
	}))

	bridge, err := NewBridge(BridgeConfig{
		ChannelID:       *channelID,
		KernelHTTPURL:   *kernelURL,
		KernelWSURL:     *kernelWSURL,
		NapCatWSURL:     wsURL,
		NapCatListen:    listenAddr,
		NapCatToken:     token,
		FFmpegPath:      *ffmpegPath,
		MaxForwardDepth: *maxForwardDepth,
		ActionTimeout:   *actionTimeout,
		RPCTimeout:      *rpcTimeout,
		InitialBackoff:  defaultInitialBackoff,
		MaxBackoff:      defaultMaxBackoff,
		Logger:          logger,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting napcat-adapter",
		"channel_id", *channelID,
		"kernel_url", *kernelURL,
		"napcat_ws_url", wsURL,
		"napcat_listen", listenAddr,
	)

	if err := bridge.Run(ctx); err != nil {
		return err
	}
	logger.Info("napcat-adapter stopped cleanly")
	return nil
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
