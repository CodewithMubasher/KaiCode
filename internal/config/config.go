package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	UI       UIConfig       `toml:"ui"`
	Provider ProviderConfig `toml:"provider"`
	Session  SessionConfig  `toml:"session"`
	Log      LogConfig      `toml:"log"`
}

type UIConfig struct {
	Theme string `toml:"theme"`
	Mouse bool   `toml:"mouse"`
}

type ProviderConfig struct {
	// Name is "deepseek" (the Chrome extension bridge) or "mock" (a scripted
	// demo that needs no browser).
	Name string `toml:"name"`
	// Addr is where the bridge listens for the Chrome extension.
	Addr string `toml:"addr"`
}

type SessionConfig struct {
	Dir string `toml:"dir"`
}

type LogConfig struct {
	Level string `toml:"level"`
	File  string `toml:"file"`
}

// KaiDir is Kai's data directory (~/.kai).
func KaiDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kai")
}

func Default() *Config {
	kaiDir := KaiDir()
	return &Config{
		UI:       UIConfig{Theme: "default", Mouse: false},
		Provider: ProviderConfig{Name: "deepseek", Addr: "127.0.0.1:8765"},
		Session:  SessionConfig{Dir: filepath.Join(kaiDir, "sessions")},
		Log:      LogConfig{Level: "debug", File: filepath.Join(kaiDir, "logs.txt")},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := Save(cfg, path); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, err
	}
	// Older configs point at logs/kai.log with level "info". Move them to the
	// single logs.txt with full detail so problems can be analysed.
	if cfg.Log.File == filepath.Join(KaiDir(), "logs", "kai.log") {
		def := Default()
		cfg.Log.File = def.Log.File
		if cfg.Log.Level == "info" {
			cfg.Log.Level = def.Log.Level
		}
		_ = Save(cfg, path)
	}
	return cfg, nil
}

func Save(cfg *Config, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}
