package fileio

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"pvzce-site/internal/config"
)

func directoryConfig(t *testing.T) config.Files {
	t.Helper()
	root := t.TempDir()
	cfg := config.Files{
		DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "configs"),
		CacheDir: filepath.Join(root, "cache"), MaxBytes: 64,
	}
	for _, path := range []string{cfg.DataDir, cfg.ConfigDir} {
		if err := os.Mkdir(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

func TestDirectoryReadersSelectTheirOwnFiles(t *testing.T) {
	cfg := directoryConfig(t)
	writeFixture(t, filepath.Join(cfg.DataDir, "same.txt"), []byte("data"))
	writeFixture(t, filepath.Join(cfg.ConfigDir, "same.txt"), []byte("config"))
	dirs, err := NewDirectories(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dirs.Close() })
	// The cache directory did not exist before initialization.
	writeFixture(t, filepath.Join(cfg.CacheDir, "same.txt"), []byte("cache"))
	for name, reader := range map[string]*Store{"data": dirs.Data, "config": dirs.Config, "cache": dirs.Cache} {
		text, err := reader.ReadText(context.Background(), "same.txt")
		if err != nil || text != name {
			t.Fatalf("%s reader: value=%q err=%v", name, text, err)
		}
	}
	writeFixture(t, filepath.Join(cfg.ConfigDir, "settings.json"), []byte(`{"title":"example"}`))
	if _, err := dirs.Data.ReadFile(context.Background(), "settings.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("config file appeared in the data reader: %v", err)
	}
	if err := os.Symlink(cfg.ConfigDir, filepath.Join(cfg.DataDir, "config-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := dirs.Data.ReadFile(context.Background(), "config-link/settings.json"); err == nil {
		t.Fatal("data reader followed a symlink into the config directory")
	}
}

func TestOverlappingCategoriesAreRejected(t *testing.T) {
	for _, name := range []string{"equal", "nested", "symlink alias"} {
		t.Run(name, func(t *testing.T) {
			cfg := directoryConfig(t)
			switch name {
			case "equal":
				cfg.ConfigDir = cfg.DataDir
			case "nested":
				cfg.CacheDir = filepath.Join(cfg.DataDir, "nested")
			case "symlink alias":
				cfg.ConfigDir = filepath.Join(filepath.Dir(cfg.DataDir), "alias")
				if err := os.Symlink(cfg.DataDir, cfg.ConfigDir); err != nil {
					t.Fatal(err)
				}
			}
			if dirs, err := NewDirectories(cfg); err == nil {
				_ = dirs.Close()
				t.Fatal("overlapping categories were accepted")
			}
		})
	}
}

func TestMissingConfigurationDirectoryFailsInitialization(t *testing.T) {
	cfg := directoryConfig(t)
	cfg.ConfigDir = filepath.Join(filepath.Dir(cfg.DataDir), "missing-configs")
	if dirs, err := NewDirectories(cfg); err == nil {
		_ = dirs.Close()
		t.Fatal("a missing configuration directory was silently created")
	}
}
