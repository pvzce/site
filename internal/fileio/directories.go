package fileio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pvzce-site/internal/config"
)

// Directories separates business data, configuration files, and disposable cache files.
// Each reader is confined to its own directory.
type Directories struct {
	Data   *Store
	Config *Store
	Cache  *Store
}

// NewDirectories opens existing data/config directories and creates a missing cache directory.
// Equal or nested roots are rejected, including roots aliased through symlinks.
func NewDirectories(cfg config.Files) (*Directories, error) {
	roots := []string{cfg.DataDir, cfg.ConfigDir, cfg.CacheDir}
	for i, root := range roots {
		if strings.TrimSpace(root) == "" {
			return nil, fmt.Errorf("file directory must not be empty")
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve file directory: %w", err)
		}
		roots[i] = absolute
	}
	if err := independentRoots(roots); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.CacheDir, 0750); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	for i, root := range roots {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("resolve file directory: %w", err)
		}
		roots[i] = resolved
	}
	if err := independentRoots(roots); err != nil {
		return nil, err
	}
	dirs := &Directories{}
	for i, target := range []**Store{&dirs.Data, &dirs.Config, &dirs.Cache} {
		store, err := New(roots[i], cfg.MaxBytes)
		if err != nil {
			_ = dirs.Close()
			return nil, err
		}
		*target = store
	}
	return dirs, nil
}

func (d *Directories) Close() error {
	var errs []error
	for _, store := range []*Store{d.Data, d.Config, d.Cache} {
		if store != nil {
			errs = append(errs, store.Close())
		}
	}
	return errors.Join(errs...)
}

func independentRoots(roots []string) error {
	for i, root := range roots {
		for j, candidate := range roots {
			if i == j {
				continue
			}
			relative, err := filepath.Rel(root, candidate)
			if err != nil {
				continue // Different volumes cannot contain each other.
			}
			if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("data, config, and cache directories must not overlap")
			}
		}
	}
	return nil
}
