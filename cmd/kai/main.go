package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"kai/internal/config"
	"kai/internal/logging"
	"kai/internal/store"
	"kai/internal/ui"
	"kai/internal/version"
)

var (
	cfgPath  string
	logLevel string
)

func configPath() string {
	if cfgPath != "" {
		return cfgPath
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kai", "config.toml")
}

var rootCmd = &cobra.Command{
	Use:   "kai",
	Short: "Kai Code — web chat agent harness (DeepSeek, Qwen)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(configPath())
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
		logger.Info("kai starting", "version", version.Version, "os", runtime.GOOS, "arch", runtime.GOARCH,
			"provider", cfg.Provider.Name, "addr", cfg.Provider.Addr, "config", configPath(), "log", cfg.Log.File)

		defer func() {
			if r := recover(); r != nil {
				logger.Error("kai panicked", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				panic(r)
			}
		}()

		// Sessions, chats and custom providers live in one SQLite file. If it
		// cannot be opened Kai still runs, it just cannot save anything.
		st, err := store.Open(cfg.Storage.Path)
		if err != nil {
			logger.Error("could not open the database: sessions and providers will not be saved", "path", cfg.Storage.Path, "err", err)
			st = nil
		} else {
			defer st.Close()
		}

		p := tea.NewProgram(ui.NewModel(cfg, st))
		if _, err := p.Run(); err != nil {
			logger.Error("tui crashed", "err", err)
			return err
		}
		logger.Info("kai exited")
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

var logsLines int

// logsCmd prints where logs.txt is and its last lines, to paste into a bug report.
var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Print the path of logs.txt and its last lines",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(configPath())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		fmt.Println("Log file:", cfg.Log.File)
		f, err := os.Open(cfg.Log.File)
		if err != nil {
			fmt.Println("(no log yet: run kai first)")
			return nil
		}
		defer f.Close()
		var ring []string
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			ring = append(ring, sc.Text())
			if len(ring) > logsLines {
				ring = ring[1:]
			}
		}
		for _, l := range ring {
			fmt.Println(l)
		}
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", "", "config file path")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "", "log level (debug/info/warn/error)")
	logsCmd.Flags().IntVarP(&logsLines, "lines", "n", 60, "how many lines to show")
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(logsCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
