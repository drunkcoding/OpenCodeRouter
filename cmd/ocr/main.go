package main

import (
	"context"
	"fmt"
	"os"

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

			app := tui.NewApp(cfg, nil, nil)
			program := tea.NewProgram(app)
			if _, err := program.Run(); err != nil {
				return fmt.Errorf("run tui: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&cfgPath, "config", "", "Path to remote-tui.yaml")
	return cmd
}
