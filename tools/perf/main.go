// Command perf is Bindery's performance harness. It seeds a synthetic library
// through the db package, boots the real binary against it with in-process
// fake indexers and a fake download client, drives the API, and writes one
// results directory per run. See docs/performance-testing.md.
//
//	go run ./tools/perf seed -books 10000 -data /tmp/perf-data
//	go run ./tools/perf run -mode quick -data /tmp/perf-data
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	// The db package logs every migration it applies through slog. The harness
	// reports through its own log lines.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: perf <seed|run> [flags]")
	}
	logf := func(format string, a ...any) {
		_, _ = fmt.Fprintf(out, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
	}
	switch args[0] {
	case "seed":
		fs := flag.NewFlagSet("seed", flag.ContinueOnError)
		fs.SetOutput(out)
		books := fs.Int("books", 1000, "number of books")
		seed := fs.Uint64("seed", 1, "random seed")
		data := fs.String("data", "", "data directory (required)")
		untracked := fs.Bool("untracked", false, "leave on-disk books untracked, as before a first scan")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *data == "" {
			return errors.New("seed: -data is required")
		}
		abs, err := filepath.Abs(*data)
		if err != nil {
			return err
		}
		_, err = Seed(ctx, seedDir(abs, *books, *untracked), SeedSpec{Books: *books, Seed: *seed, Untracked: *untracked, LibraryDir: libraryDir(abs, *books, *untracked)}, logf)
		return err
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		fs.SetOutput(out)
		cfg := Config{}
		mode := fs.String("mode", "quick", "quick, full or tiny")
		fs.StringVar(&cfg.DataDir, "data", "", "data directory for seeds, the built binary and results (required)")
		fs.StringVar(&cfg.Binary, "binary", "", "bindery binary to drive; built from this checkout when empty")
		fs.StringVar(&cfg.OutDir, "out", "", "results directory; <data>/results/<timestamp>-<mode> when empty")
		fs.StringVar(&cfg.Only, "only", "", "regular expression; run only the scenario groups it matches")
		fs.DurationVar(&cfg.IndexerLatency, "indexer-latency", 50*time.Millisecond, "delay before every fake indexer response")
		fs.DurationVar(&cfg.ClientLatency, "client-latency", 5*time.Millisecond, "delay before every fake download client response")
		fs.Uint64Var(&cfg.Seed, "seed", 1, "random seed for the data and the request order")
		fs.StringVar(&cfg.LogLevel, "log-level", "info", "log level of the binary under test; debug is for diagnosing the harness, not for measuring")
		fs.BoolVar(&cfg.NoProfiles, "no-profiles", false, "skip the CPU and heap profile pass")
		reps := fs.Int("reps", 0, "repetitions per scenario; the mode's own number (3 for quick and full) when 0")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if cfg.DataDir == "" {
			return errors.New("run: -data is required")
		}
		abs, err := filepath.Abs(cfg.DataDir)
		if err != nil {
			return err
		}
		cfg.DataDir = abs
		m, err := modeByName(*mode)
		if err != nil {
			return err
		}
		if *reps > 0 {
			m.Reps = *reps
		}
		cfg.Mode = m
		return Run(ctx, cfg, logf)
	default:
		return fmt.Errorf("unknown command %q (want seed or run)", args[0])
	}
}

func seedDir(data string, books int, untracked bool) string {
	name := fmt.Sprintf("seed-%d", books)
	if untracked {
		name += "-untracked"
	}
	return filepath.Join(data, name)
}

func libraryDir(data string, books int, untracked bool) string {
	name := fmt.Sprintf("library-%d", books)
	if untracked {
		name += "-untracked"
	}
	return filepath.Join(data, name)
}
