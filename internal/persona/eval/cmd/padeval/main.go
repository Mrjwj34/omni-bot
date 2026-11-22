package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"omni-bot/internal/envload"
	"omni-bot/internal/persona/eval"
	"omni-bot/internal/persona/pad"
)

func main() {
	var (
		envPath    = flag.String("env", "", "optional path to .env.local (defaults to searching repo root)")
		outReport  = flag.String("out", "", "optional path to write Markdown benchmark report")
		outJSON    = flag.String("json-out", "", "optional path to write raw JSON benchmark results")
		timeout    = flag.Duration("timeout", 10*time.Minute, "overall timeout for live evaluation")
		chatModel  = flag.String("chat-model", "", "override GEMINI_CHAT_MODEL")
		judgeModel = flag.String("judge-model", "", "override judge model (defaults to chat model)")
		decModel   = flag.String("decision-model", "", "override DECISION_MODEL")
	)
	flag.Parse()

	if *envPath != "" {
		if err := envload.LoadFile(*envPath); err != nil {
			fatal("load env file %s: %v", *envPath, err)
		}
	} else {
		if _, err := envload.LoadRepoEnv(""); err != nil {
			fatal("load repo env: %v", err)
		}
	}

	cfg := eval.ClientConfig{
		GeminiAPIKey:    os.Getenv("GEMINI_API_KEY"),
		GeminiBaseURL:   os.Getenv("GEMINI_API_BASE"),
		ChatModel:       firstNonEmpty(*chatModel, os.Getenv("GEMINI_CHAT_MODEL"), "gemini-4-hydrogen"),
		JudgeModel:      firstNonEmpty(*judgeModel, *chatModel, os.Getenv("GEMINI_CHAT_MODEL"), "gemini-4-hydrogen"),
		DecisionAPIKey:  os.Getenv("DECISION_API_KEY"),
		DecisionBaseURL: os.Getenv("DECISION_API_BASE"),
		DecisionModel:   firstNonEmpty(*decModel, os.Getenv("DECISION_MODEL"), "models/gemini-decision-1"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client, err := eval.NewClient(ctx, cfg)
	if err != nil {
		fatal("init eval client: %v", err)
	}

	report, err := eval.RunAll(ctx, client, pad.DefaultTemperament(), func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})
	if err != nil {
		fatal("run live eval: %v", err)
	}

	if *outJSON != "" {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fatal("marshal json: %v", err)
		}
		if err := writeAtomically(*outJSON, raw); err != nil {
			fatal("write json %s: %v", *outJSON, err)
		}
		fmt.Fprintf(os.Stderr, "Wrote raw JSON to %s\n", *outJSON)
	}

	md := eval.RenderMarkdownReport(report)
	if *outReport != "" {
		if err := writeAtomically(*outReport, []byte(md)); err != nil {
			fatal("write markdown %s: %v", *outReport, err)
		}
		fmt.Fprintf(os.Stderr, "Wrote Markdown report to %s\n", *outReport)
	} else {
		fmt.Print(md)
	}
}

func writeAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "padeval: "+format+"\n", args...)
	os.Exit(1)
}
