package npm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// scanner reads a JSON document as a stream, so that the base64 tarball of a publish goes to the
// spool without the whole string in memory. It reads values whole (raw) or a string as a stream
// of its decoded bytes (streamString).
type scanner struct {
	r *bufio.Reader
}

func newScanner(r io.Reader) *scanner { return &scanner{r: bufio.NewReaderSize(r, 64<<10)} }

var errSyntax = errors.New("the body is not valid JSON")

// peek returns the next byte after white space without reading it.
func (s *scanner) peek() (byte, error) {
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return 0, errSyntax
		}
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c, s.r.UnreadByte()
	}
}

// expect reads the byte c after white space.
func (s *scanner) expect(c byte) error {
	got, err := s.peek()
	if err != nil {
		return err
	}
	if got != c {
		return errSyntax
	}
	_, err = s.r.ReadByte()
	return err
}

// more reports, inside an object or an array, whether another member follows (reading the ","),
// or the end (reading the closing byte end).
func (s *scanner) more(first bool, end byte) (bool, error) {
	c, err := s.peek()
	if err != nil {
		return false, err
	}
	if c == end {
		s.r.ReadByte()
		return false, nil
	}
	if !first {
		if c != ',' {
			return false, errSyntax
		}
		s.r.ReadByte()
	}
	return true, nil
}

// key reads an object key and the ":" after it.
func (s *scanner) key(limit int) (string, error) {
	raw, err := s.raw(limit)
	if err != nil {
		return "", err
	}
	var k string
	if json.Unmarshal(raw, &k) != nil {
		return "", errSyntax
	}
	return k, s.expect(':')
}

// raw reads one value whole, at most limit bytes.
func (s *scanner) raw(limit int) (json.RawMessage, error) {
	c, err := s.peek()
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	depth, inString, escaped := 0, false, false
	for {
		c, err = s.r.ReadByte()
		if err != nil {
			return nil, errSyntax
		}
		if b.Len() >= limit {
			return nil, fmt.Errorf("a value of the body is larger than %d bytes", limit)
		}
		b.WriteByte(c)
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
				if depth == 0 {
					return valid(b.Bytes())
				}
			}
			continue
		case c == '"':
			inString = true
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
			if depth == 0 {
				return valid(b.Bytes())
			}
			if depth < 0 {
				return nil, errSyntax
			}
		}
		if depth == 0 && !inString {
			// A number or a literal: it ends before a delimiter.
			n, err := s.r.Peek(1)
			if err != nil || bytes.ContainsAny(n, ",}] \t\r\n") {
				return valid(b.Bytes())
			}
		}
	}
}

func valid(b []byte) (json.RawMessage, error) {
	if !json.Valid(b) {
		return nil, errSyntax
	}
	return json.RawMessage(bytes.Clone(b)), nil
}

// streamString reads a string value and writes its decoded bytes to w.
func (s *scanner) streamString(w io.Writer) error {
	if err := s.expect('"'); err != nil {
		return err
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return errSyntax
		}
		switch {
		case c == '"':
			return bw.Flush()
		case c < 0x20:
			return errSyntax
		case c != '\\':
			if err := bw.WriteByte(c); err != nil {
				return err
			}
			continue
		}
		e, err := s.r.ReadByte()
		if err != nil {
			return errSyntax
		}
		var out byte
		switch e {
		case '"', '\\', '/':
			out = e
		case 'b':
			out = '\b'
		case 'f':
			out = '\f'
		case 'n':
			out = '\n'
		case 'r':
			out = '\r'
		case 't':
			out = '\t'
		case 'u':
			hex := make([]byte, 4)
			if _, err := io.ReadFull(s.r, hex); err != nil {
				return errSyntax
			}
			n, err := strconv.ParseUint(string(hex), 16, 16)
			if err != nil {
				return errSyntax
			}
			if _, err := bw.WriteString(string(rune(n))); err != nil {
				return err
			}
			continue
		default:
			return errSyntax
		}
		if err := bw.WriteByte(out); err != nil {
			return err
		}
	}
}
