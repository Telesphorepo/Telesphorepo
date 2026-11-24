// Package namesearch finds the names that hold a text, in any case and at any length of the text.
// Each searchable table has a gram table of its own: one row per distinct gram of each folded
// name (every substring of 1 to MaxGram characters), keyed by the gram and then the name, so the
// names that hold a gram are one range of an index in name order. A text of up to MaxGram
// characters is one gram: its rows are the answer. A longer text reads the rows of its rarest
// trigram (Gram, a bounded probe) and keeps the names that hold the text (instr on the gram row's
// name), which reads no other row.
//
// A search lists the names that start with the text first, then the others, each part in folded
// name order (Compare). A page reads the ranges of Steps in order, so its cursor is the last name
// of the page alone: its folded form says which range it lies in. The owner of each table writes
// the grams of a name (Grams) in the transaction of each write of the name.
package namesearch

import (
	"cmp"
	"context"
	"strings"
	"unicode/utf8"
)

const (
	// MaxGram is the longest gram: a text of up to MaxGram characters is one gram.
	MaxGram = 3
	// Probe is how many rows a probe of a trigram counts to: a rarer one counts under it.
	Probe = 1000
	// MaxText is the longest text a search takes, in characters: no searchable name or title is
	// longer, so a longer text matches nothing.
	MaxText = 256
	// Top is above every folded name (no UTF-8 string has the byte 0xF5): the end of the last range.
	Top = "\xf5"
)

// Fold returns s with its ASCII letters in lower case, as SQLite's lower() does, so the grams that
// Go writes and those a migration writes with lower() are the same.
func Fold(s string) string {
	for i := range len(s) {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// Text is the text of a search from the raw query: trimmed and folded. "" lists every name.
func Text(raw string) string { return Fold(strings.TrimSpace(raw)) }

// Grams returns the distinct grams of the folded name: its substrings of 1 to MaxGram characters.
func Grams(name string) []string {
	r := []rune(Fold(name))
	seen := make(map[string]bool, MaxGram*len(r))
	var out []string
	for n := 1; n <= MaxGram; n++ {
		for i := 0; i+n <= len(r); i++ {
			if g := string(r[i : i+n]); !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

// Count counts the rows of gram in a gram table, up to limit.
type Count func(ctx context.Context, gram string, limit int64) (int64, error)

// Gram returns the gram whose rows a search of q reads: q itself when it has at most MaxGram
// characters, else the trigram of q that the fewest rows hold. Each probe counts up to Probe rows,
// then up to the fewest so far, so a probe never reads more than Probe rows at any size. ok is false
// when no name can hold q: a trigram without a row, or q longer than MaxText.
func Gram(ctx context.Context, q string, count Count) (gram string, ok bool, err error) {
	n := utf8.RuneCountInString(q)
	switch {
	case n == 0 || n > MaxText:
		return "", false, nil
	case n <= MaxGram:
		return q, true, nil
	}
	r := []rune(q)
	fewest := int64(Probe)
	seen := map[string]bool{}
	for i := 0; i+MaxGram <= len(r); i++ {
		g := string(r[i : i+MaxGram])
		if seen[g] {
			continue
		}
		seen[g] = true
		rows, err := count(ctx, g, fewest)
		if err != nil {
			return "", false, err
		}
		if rows == 0 {
			return "", false, nil // no name holds g, so none holds q
		}
		if gram == "" || rows < fewest {
			gram, fewest = g, rows
		}
	}
	return gram, true, nil
}

// A Step is one range of folded names that a page reads: [From, To), after the name After when
// the page starts inside it ("" from From).
type Step struct {
	From, To string
	After    string
}

// Lower is the least name of the step for a table whose names are folded (a repo, a username):
// From, or the first name after After.
func (s Step) Lower() string {
	if a := s.After + "\x00"; s.After != "" && a > s.From {
		return a
	}
	return s.From
}

// Key is the key (folded name, name) that the rows of the step follow, for a table that orders by
// the folded name and then the name: (From, "") or the key of After.
func (s Step) Key() (folded, name string) {
	if s.After == "" {
		return s.From, ""
	}
	return Fold(s.After), s.After
}

// above returns the least string above every string that starts with q (q is not empty).
func above(q string) string {
	b := []byte(q)
	b[len(b)-1]++
	return string(b)
}

// Steps returns the ranges that the page of a search of q after the name after ("" for the first
// page) reads, in order: the names that start with q, then the names before them, then the names
// after them.
func Steps(q, after string) []Step {
	hi := above(q)
	starts, before, rest := Step{From: q, To: hi}, Step{To: q}, Step{From: hi, To: Top}
	if after == "" {
		return []Step{starts, before, rest}
	}
	switch a := Fold(after); {
	case a >= q && a < hi:
		starts.After = after
		return []Step{starts, before, rest}
	case a < q:
		before.After = after
		return []Step{before, rest}
	default:
		rest.After = after
		return []Step{rest}
	}
}

// Compare orders the names a and b as a search of q lists them: the names that start with q
// first, then by folded name, then by name. A merge of the pages of several lists sorts by it.
func Compare(q, a, b string) int {
	fa, fb := Fold(a), Fold(b)
	if sa, sb := strings.HasPrefix(fa, q), strings.HasPrefix(fb, q); sa != sb {
		if sa {
			return -1
		}
		return 1
	}
	return cmp.Or(strings.Compare(fa, fb), strings.Compare(a, b))
}

// Page returns at most limit items of a search of q after the name after: the steps of Steps in
// order, each read by read (at most the items the page still lacks) until the page is full.
func Page[T any](ctx context.Context, q, after string, limit int, read func(ctx context.Context, st Step, limit int) ([]T, error)) ([]T, error) {
	var out []T
	for _, st := range Steps(q, after) {
		if len(out) >= limit {
			break
		}
		items, err := read(ctx, st, limit-len(out))
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}
