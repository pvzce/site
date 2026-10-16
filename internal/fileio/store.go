package fileio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidPath = errors.New("invalid relative file path")
	ErrNotRegular  = errors.New("path is not a regular file")
	ErrTooLarge    = errors.New("file exceeds size limit")
	ErrInvalidText = errors.New("file is not valid UTF-8 text")
)

// Store provides internal, read-only access within a configured directory.
// os.Root prevents path and symlink traversal outside that directory.
type Store struct {
	root     *os.Root
	maxBytes int64
}

func New(rootPath string, maxBytes int64) (*Store, error) {
	if maxBytes < 1 || maxBytes > 1<<30 {
		return nil, fmt.Errorf("file size limit must be between 1 and 1073741824")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open file root: %w", err)
	}
	return &Store{root: root, maxBytes: maxBytes}, nil
}

func (s *Store) Close() error {
	return s.root.Close()
}

// Open returns an owned file handle; the caller must close it.
func (s *Store) Open(ctx context.Context, name string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\\x00:") {
		return nil, ErrInvalidPath
	}
	// Avoid opening known non-regular files such as FIFOs and devices.
	info, err := s.root.Stat(name)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	file, err := s.root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > s.maxBytes {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, ErrNotRegular
		}
		return nil, ErrTooLarge
	}
	return file, nil
}

// ReadFile bounds memory use and checks cancellation between reads.
func (s *Store) ReadFile(ctx context.Context, name string) ([]byte, error) {
	file, err := s.Open(ctx, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := contextReader{ctx: ctx, reader: file}
	body, err := io.ReadAll(io.LimitReader(reader, s.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > s.maxBytes {
		return nil, ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return body, nil
}

func (s *Store) ReadText(ctx context.Context, name string) (string, error) {
	body, err := s.ReadFile(ctx, name)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(body) {
		return "", ErrInvalidText
	}
	return string(body), nil
}

// ReadJSON decodes one JSON value; callers validate its business meaning.
func (s *Store) ReadJSON(ctx context.Context, name string, destination any) error {
	body, err := s.ReadFile(ctx, name)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("decode JSON file: %w", err)
	}
	return ctx.Err()
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
