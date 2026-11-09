package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyLogPathIsMigrated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	p := filepath.Join(home, ".kai", "config.toml")
	os.MkdirAll(filepath.Dir(p), 0o755)
	legacy := "[log]\nlevel = \"info\"\nfile = " + `"` + filepath.ToSlash(filepath.Join(home, ".kai", "logs", "kai.log")) + `"` + "\n"
	os.WriteFile(p, []byte(legacy), 0o644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cfg.Log.File) != "logs.txt" || cfg.Log.Level != "debug" {
		t.Errorf("log = %+v", cfg.Log)
	}
}
