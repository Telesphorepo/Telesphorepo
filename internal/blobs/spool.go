package blobs

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
)

// ErrTooLarge is a body above the size limit.
var ErrTooLarge = errors.New("upload too large")

// Spooled is a body with its size and hashes (lowercase hex), computed in one pass: a temp file
// on the data volume (File), or bytes in memory (Data) for a small generated file. Close
// removes the temp file.
type Spooled struct {
	File   *os.File
	Data   []byte
	Size   int64
	SHA256 string
	SHA1   string
	MD5    string
}

// Close removes the temp file of s.
func (s *Spooled) Close() error {
	if s.File == nil {
		return nil
	}
	name := s.File.Name()
	s.File.Close()
	s.File = nil
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Reader returns a reader of the whole body from its start.
func (s *Spooled) Reader() (io.ReadSeeker, error) {
	if s.File == nil {
		if s.Data != nil {
			return bytes.NewReader(s.Data), nil
		}
		return nil, os.ErrClosed
	}
	if _, err := s.File.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return s.File, nil
}

// Spool copies r to a temp file in dir with its hashes. A body above max bytes is ErrTooLarge.
func Spool(dir string, r io.Reader, max int64) (*Spooled, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "upload-*")
	if err != nil {
		return nil, err
	}
	sp := &Spooled{File: f}
	h256, h1, h5 := sha256.New(), sha1.New(), md5.New()
	n, err := io.Copy(io.MultiWriter(f, h256, h1, h5), io.LimitReader(r, max+1))
	if err != nil {
		sp.Close()
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, ErrTooLarge
		}
		return nil, err
	}
	if n > max {
		sp.Close()
		return nil, ErrTooLarge
	}
	sp.Size = n
	sp.SHA256 = hex.EncodeToString(h256.Sum(nil))
	sp.SHA1 = hex.EncodeToString(h1.Sum(nil))
	sp.MD5 = hex.EncodeToString(h5.Sum(nil))
	return sp, nil
}

// Hash reads the whole of f from its start and returns it as a Spooled with its hashes: the
// end of a resumable upload.
func Hash(f *os.File) (*Spooled, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	h256, h1, h5 := sha256.New(), sha1.New(), md5.New()
	n, err := io.Copy(io.MultiWriter(h256, h1, h5), f)
	if err != nil {
		return nil, err
	}
	return &Spooled{File: f, Size: n, SHA256: hex.EncodeToString(h256.Sum(nil)),
		SHA1: hex.EncodeToString(h1.Sum(nil)), MD5: hex.EncodeToString(h5.Sum(nil))}, nil
}

// Bytes returns b as a Spooled in memory with its hashes: a generated file such as a checksum or
// an index.
func Bytes(b []byte) *Spooled {
	s256, s1, s5 := sha256.Sum256(b), sha1.Sum(b), md5.Sum(b)
	return &Spooled{Data: b, Size: int64(len(b)), SHA256: hex.EncodeToString(s256[:]),
		SHA1: hex.EncodeToString(s1[:]), MD5: hex.EncodeToString(s5[:])}
}
