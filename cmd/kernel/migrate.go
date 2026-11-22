package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"omni-bot/internal/store"
	"omni-bot/internal/store/legacy"
)

func runMigrateLegacy(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("migrate-legacy", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fromDir := fs.String("from", "", "Read-only legacy AstrBot data directory (never modified)")
	dataDir := fs.String("data", "./data", "Target omni-bot data directory")
	channelID := fs.String("channel", "napcat", "Default channel_id for migrated timeline records")
	asJSON := fs.Bool("json", false, "Print migration report as JSON")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if strings.TrimSpace(*fromDir) == "" {
		return errors.New("migrate-legacy requires --from <legacy_dir>")
	}
	if strings.TrimSpace(*dataDir) == "" {
		return errors.New("migrate-legacy requires --data <target_dir>")
	}

	st, err := store.Open(ctx, filepath.Join(*dataDir, "omni.db"))
	if err != nil {
		return fmt.Errorf("open target store: %w", err)
	}
	defer func() { _ = st.Close() }()

	blobs, err := store.NewBlobStore(*dataDir)
	if err != nil {
		return fmt.Errorf("init target blob store: %w", err)
	}

	report, err := legacy.MigrateLegacy(ctx, legacy.Options{
		SourceDir:        *fromDir,
		TargetStore:      st,
		TargetBlobs:      blobs,
		DefaultChannelID: *channelID,
	})
	if err != nil {
		return fmt.Errorf("migrate legacy assets: %w", err)
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	fmt.Fprintf(stdout, "Legacy migration complete (source untouched: %s -> %s)\n", *fromDir, *dataDir)
	fmt.Fprintf(stdout, "  Timeline messages : %d\n", report.TimelineImported)
	fmt.Fprintf(stdout, "  Room summaries    : %d\n", report.SummariesImported)
	fmt.Fprintf(stdout, "  Memes metadata    : %d (files: %d, thumbs: %d, seen: %d)\n",
		report.MemesImported, report.MemeFilesCopied, report.MemeThumbsCopied, report.MemeSeenImported)
	fmt.Fprintf(stdout, "  Memories (mem0)   : %d\n", report.MemoriesImported)
	fmt.Fprintf(stdout, "  Persona imported  : %v (traits seeded: %d)\n", report.PersonaImported, report.PersonaTraitsSeeded)
	if len(report.Warnings) > 0 {
		fmt.Fprintln(stdout, "  Warnings:")
		for _, w := range report.Warnings {
			fmt.Fprintf(stdout, "    - %s\n", w)
		}
	}
	return nil
}
