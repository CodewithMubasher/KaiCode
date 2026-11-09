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
	Name string `toml:"name"`
}

type SessionConfig struct {
	Dir string `toml:"dir"`
}

type LogConfig struct {
	Level string `toml:"level"`
	File  string `toml:"file"`
}

func Default() *Config {
	home, _ := os.UserHomeDir()
	kaiDir := filepath.Join(home, ".kai")
	return &Config{
		UI:       UIConfig{Theme: "default", Mouse: false},
		Provider: ProviderConfig{Name: "deepseek"},
		Session:  SessionConfig{Dir: filepath.Join(kaiDir, "sessions")},
		Log:      LogConfig{Level: "info", File: filepath.Join(kaiDir, "logs", "kai.log")},
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
