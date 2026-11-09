package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"kai/internal/config"
	"kai/internal/logging"
	"kai/internal/ui"
	"kai/internal/version"
)

var (
	cfgPath  string
	logLevel string
)

var rootCmd = &cobra.Command{
	Use:   "kai",
	Short: "Kai Code — DeepSeek WebChat agent harness",
	RunE: func(cmd *cobra.Command, args []string) error {
		home, _ := os.UserHomeDir()
		if cfgPath == "" {
			cfgPath = filepath.Join(home, ".kai", "config.toml")
		}

		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		if logLevel != "" {
			cfg.Log.Level = logLevel
		}

		logger, err := logging.Setup(cfg.Log.File, cfg.Log.Level)
		if err != nil {
			return fmt.Errorf("setup logging: %w", err)
		}
		logger.Info("kai starting", "version", version.Version)

		p := tea.NewProgram(ui.NewModel(cfg))
		if _, err := p.Run(); err != nil {
			logger.Error("tui crashed", "err", err)
			return err
		}
		return nil
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Kai Code", version.Version)
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", "", "config file path")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "", "log level (debug/info/warn/error)")
	rootCmd.AddCommand(versionCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
