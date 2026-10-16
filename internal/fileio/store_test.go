package fileio

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T, maxBytes int64) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := New(root, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, root
}

func writeFixture(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadBytesTextAndJSON(t *testing.T) {
	s, root := testStore(t, 64)
	writeFixture(t, filepath.Join(root, "text.txt"), []byte("你好\n"))
	writeFixture(t, filepath.Join(root, "binary.bin"), []byte{0, 255, 1})
	writeFixture(t, filepath.Join(root, "config.json"), []byte(`{"name":"pvzce"}`))
	ctx := context.Background()
	text, err := s.ReadText(ctx, "text.txt")
	if err != nil || text != "你好\n" {
		t.Fatalf("read text: %q %v", text, err)
	}
	bytes, err := s.ReadFile(ctx, "binary.bin")
	if err != nil || len(bytes) != 3 || bytes[1] != 255 {
		t.Fatalf("read bytes: %v %v", bytes, err)
	}
	var settings struct{ Name string }
	if err := s.ReadJSON(ctx, "config.json", &settings); err != nil || settings.Name != "pvzce" {
		t.Fatalf("read JSON: %+v %v", settings, err)
	}
	if _, err := s.ReadText(ctx, "binary.bin"); !errors.Is(err, ErrInvalidText) {
		t.Fatalf("expected invalid UTF-8 error, got %v", err)
	}
}

func TestFileLimitsAndErrors(t *testing.T) {
	s, root := testStore(t, 8)
	writeFixture(t, filepath.Join(root, "exact.txt"), []byte("12345678"))
	writeFixture(t, filepath.Join(root, "large.txt"), []byte("123456789"))
	writeFixture(t, filepath.Join(root, "bad.json"), []byte("{} {}"))
	ctx := context.Background()
	if body, err := s.ReadFile(ctx, "exact.txt"); err != nil || len(body) != 8 {
		t.Fatalf("file at the exact limit should be readable: %v", err)
	}
	if _, err := s.ReadFile(ctx, "large.txt"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected file size error, got %v", err)
	}
	if _, err := s.ReadFile(ctx, "missing.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected missing file error, got %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "directory"); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("expected directory rejection, got %v", err)
	}
	var value any
	if err := s.ReadJSON(ctx, "bad.json", &value); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ReadFile(canceled, "exact.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestPathsCannotEscapeRoot(t *testing.T) {
	s, root := testStore(t, 64)
	ctx := context.Background()
	for _, path := range []string{".", "../secret", "/etc/passwd", "sub/../../secret", "sub//file", `C:\secret`, "file\x00"} {
		if _, err := s.ReadFile(ctx, path); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("path %q: expected invalid path, got %v", path, err)
		}
	}
	outside := t.TempDir()
	writeFixture(t, filepath.Join(outside, "secret.txt"), []byte("outside"))
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile(ctx, "escape/secret.txt"); err == nil {
		t.Fatal("directory symlink escaped file root")
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "external.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile(ctx, "external.txt"); err == nil {
		t.Fatal("file symlink escaped file root")
	}
	writeFixture(t, filepath.Join(root, "inside.txt"), []byte("inside"))
	if err := os.Symlink("inside.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if text, err := s.ReadText(ctx, "link.txt"); err != nil || text != "inside" {
		t.Fatalf("symlink within the root should work: %v", err)
	}
}

func TestStoreRequiresExistingRootAndPositiveLimit(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "missing"), 64); err == nil {
		t.Fatal("missing root was accepted")
	}
	if _, err := New(t.TempDir(), 0); err == nil {
		t.Fatal("zero file size limit was accepted")
	}
}
