package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"opencoderouter/internal/tui"
	"opencoderouter/internal/tui/config"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := newRootCmd()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "ocr failed: %v\n", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var cfgPath string
	var debug bool
	var logFile string

	cmd := &cobra.Command{
		Use:          "ocr",
		Aliases:      []string{"opencode-remote"},
		Short:        "Night Ops TUI for remote opencode fleets",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			cfg, err := config.Load(ctx, cfgPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			logger, closeLogFile, err := buildLogger(debug, logFile)
			if err != nil {
				return err
			}
			if closeLogFile != nil {
				defer closeLogFile()
			}
			if logger != nil {
				logger.Info("ocr logger initialized", "debug", debug, "log_file", logFile)
			}

			app := tui.NewApp(cfg, nil, nil, logger)
			program := tea.NewProgram(app)
			app.SetProgram(program)
			if _, err := program.Run(); err != nil {
				return fmt.Errorf("run tui: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&cfgPath, "config", "", "Path to remote-tui.yaml")
	cmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging")
	cmd.Flags().StringVar(&logFile, "log-file", "", "Path to log file")
	return cmd
}

func buildLogger(debug bool, logFile string) (*slog.Logger, func(), error) {
	if logFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, fmt.Errorf("resolve home dir for default log file: %w", err)
		}
		logFile = filepath.Join(home, ".ocr", "ocr.log")
	}

	if err := os.MkdirAll(filepath.Dir(logFile), 0o750); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}

	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}

	closeFn := func() {
		if closeErr := file.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "ocr: close log file %q: %v\n", logFile, closeErr)
		}
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(file, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return logger, closeFn, nil
}
