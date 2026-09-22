package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalArtifacts gives plugins a bounded, per-runtime place for downloaded
// images/videos. Plugins pass returned paths to their platform upload action.
type LocalArtifacts struct{ root string }

func NewLocalArtifacts(root string) (*LocalArtifacts, error) {
	if root == "" {
		root = "data/artifacts"
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &LocalArtifacts{root: root}, nil
}
func (s *LocalArtifacts) path(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		name = "artifact"
	}
	return filepath.Join(s.root, name)
}
func (s *LocalArtifacts) Save(ctx context.Context, name string, r io.Reader) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	p := s.path(name)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err = io.Copy(f, r); err != nil {
		return "", err
	}
	return p, nil
}
func (s *LocalArtifacts) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	f, err := os.Open(s.path(name))
	if err != nil {
		return nil, err
	}
	return f, nil
}
func (s *LocalArtifacts) Remove(_ context.Context, name string) error {
	p := s.path(name)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove artifact: %w", err)
	}
	return nil
}
