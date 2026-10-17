package workshop

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Maintenance reclaims bounded batches every minute. No objects referenced by a file are deleted.
func (s *Service) Maintenance(ctx context.Context, logger *slog.Logger) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if e := s.Reap(ctx); e != nil && ctx.Err() == nil {
			logger.Error("upload reservation maintenance failed")
		}
		s.cleanSpool()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (s *Service) cleanSpool() {
	dir := os.Getenv("UPLOAD_TEMP_DIR")
	if dir == "" {
		dir = "cache/uploads"
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "upload-") || !entry.Type().IsRegular() {
			continue
		}
		info, e := entry.Info()
		if e == nil && time.Since(info.ModTime()) > 20*time.Minute {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
