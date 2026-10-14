package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type fsStore struct{ root string }

// NewFS returns a filesystem Store rooted at root (spec §5.1: temp file + rename).
func NewFS(root string) (Store, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	return &fsStore{root: root}, nil
}

const tmpPrefix = ".tmp-"

func (s *fsStore) path(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("storage: empty key")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." {
			return "", fmt.Errorf("storage: invalid key %q", key)
		}
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

func (s *fsStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			os.Remove(tmp)
		}
	}()
	n, err := io.Copy(f, r)
	if err == nil && n != size {
		err = fmt.Errorf("storage: wrote %d bytes, expected %d", n, size)
	}
	if err == nil {
		err = f.Chmod(0o640)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	tmp = ""
	return nil
}

func (s *fsStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, ObjectInfo{}, mapErr(err)
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		if err == nil {
			err = ErrNotFound
		}
		return nil, ObjectInfo{}, mapErr(err)
	}
	return f, ObjectInfo{Key: key, Size: st.Size(), ModTime: st.ModTime()}, nil
}

func (s *fsStore) Head(ctx context.Context, key string) (ObjectInfo, error) {
	p, err := s.path(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return ObjectInfo{}, mapErr(err)
	}
	if st.IsDir() {
		return ObjectInfo{}, ErrNotFound
	}
	return ObjectInfo{Key: key, Size: st.Size(), ModTime: st.ModTime()}, nil
}

func (s *fsStore) Delete(ctx context.Context, key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *fsStore) List(ctx context.Context, prefix, after string, limit int) ([]ObjectInfo, error) {
	start := s.root
	if i := strings.LastIndex(prefix, "/"); i >= 0 {
		p, err := s.path(prefix[:i])
		if err != nil {
			return nil, err
		}
		start = p
	}
	var out []ObjectInfo
	err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), tmpPrefix) {
			return ctx.Err()
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		key := path.Clean(filepath.ToSlash(rel))
		if !strings.HasPrefix(key, prefix) || key <= after {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		out = append(out, ObjectInfo{Key: key, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fsStore) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	return "", false, nil
}

func mapErr(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}
