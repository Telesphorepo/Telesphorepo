package blobs

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
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

	// adopted: the filesystem store renamed File into place as the blob object (Store.EnsureAll),
	// so File reads the object and Close only closes it.
	adopted bool
}

// Close removes the temp file of s.
func (s *Spooled) Close() error {
	if s.File == nil {
		return nil
	}
	name := s.File.Name()
	s.File.Close()
	s.File = nil
	if s.adopted {
		return nil
	}
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ReaderAt returns the body for reads at any offset, safe from several goroutines at once.
func (s *Spooled) ReaderAt() (io.ReaderAt, error) {
	if s.File == nil {
		if s.Data != nil {
			return bytes.NewReader(s.Data), nil
		}
		return nil, os.ErrClosed
	}
	return s.File, nil
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

// Hash reads the whole of f from its start and returns it as a Spooled with its hashes.
func Hash(f *os.File) (*Spooled, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	h := NewHasher()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Spooled(f), nil
}

// Hasher computes the size, SHA-256, SHA-1 and MD5 of a body that arrives in pieces (a resumable
// upload). Its state is saved between the pieces (MarshalBinary), so the end of the body needs
// no second read of it.
type Hasher struct {
	n            int64
	h256, h1, h5 hash.Hash
}

// NewHasher returns a Hasher of an empty body.
func NewHasher() *Hasher {
	return &Hasher{h256: sha256.New(), h1: sha1.New(), h5: md5.New()}
}

// Write adds p to the body.
func (h *Hasher) Write(p []byte) (int, error) {
	h.h256.Write(p)
	h.h1.Write(p)
	h.h5.Write(p)
	h.n += int64(len(p))
	return len(p), nil
}

// Size is the bytes written so far.
func (h *Hasher) Size() int64 { return h.n }

// Spooled returns f, the file of the body, with the size and hashes of h.
func (h *Hasher) Spooled(f *os.File) *Spooled {
	return &Spooled{File: f, Size: h.n, SHA256: hex.EncodeToString(h.h256.Sum(nil)),
		SHA1: hex.EncodeToString(h.h1.Sum(nil)), MD5: hex.EncodeToString(h.h5.Sum(nil))}
}

// MarshalBinary encodes the state: the size, the three hash states with their lengths, and the
// SHA-256 of all of that, so UnmarshalBinary refuses a torn or damaged state.
func (h *Hasher) MarshalBinary() ([]byte, error) {
	b := binary.BigEndian.AppendUint64(nil, uint64(h.n))
	for _, x := range []hash.Hash{h.h256, h.h1, h.h5} {
		st, err := x.(encoding.BinaryMarshaler).MarshalBinary()
		if err != nil {
			return nil, err
		}
		b = binary.BigEndian.AppendUint32(b, uint32(len(st)))
		b = append(b, st...)
	}
	sum := sha256.Sum256(b)
	return append(b, sum[:]...), nil
}

// errHasherState is a saved state that does not decode.
var errHasherState = errors.New("blobs: damaged hash state")

// UnmarshalBinary restores a state that MarshalBinary encoded.
func (h *Hasher) UnmarshalBinary(b []byte) error {
	if len(b) < 8+sha256.Size {
		return errHasherState
	}
	body, sum := b[:len(b)-sha256.Size], b[len(b)-sha256.Size:]
	if want := sha256.Sum256(body); !bytes.Equal(sum, want[:]) {
		return errHasherState
	}
	n := NewHasher()
	n.n = int64(binary.BigEndian.Uint64(body))
	body = body[8:]
	for _, x := range []hash.Hash{n.h256, n.h1, n.h5} {
		if len(body) < 4 {
			return errHasherState
		}
		l := binary.BigEndian.Uint32(body)
		body = body[4:]
		if uint64(len(body)) < uint64(l) {
			return errHasherState
		}
		if err := x.(encoding.BinaryUnmarshaler).UnmarshalBinary(body[:l]); err != nil {
			return errHasherState
		}
		body = body[l:]
	}
	if len(body) != 0 || n.n < 0 {
		return errHasherState
	}
	*h = *n
	return nil
}

// Bytes returns b as a Spooled in memory with its hashes: a generated file such as a checksum or
// an index.
func Bytes(b []byte) *Spooled {
	s256, s1, s5 := sha256.Sum256(b), sha1.Sum(b), md5.Sum(b)
	return &Spooled{Data: b, Size: int64(len(b)), SHA256: hex.EncodeToString(s256[:]),
		SHA1: hex.EncodeToString(s1[:]), MD5: hex.EncodeToString(s5[:])}
}
