package db

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// A list is a set of values that a query takes as one parameter: json_each(?) of a JSON array, or
// IN (?, ?, ...) of a slice. Its cost should follow the list, so the list drives the plan: it is
// the outer loop of its join, or it keys the SEARCH of the IN that reads it. Otherwise SQLite reads
// a range by a shorter prefix (every row of a repository) and filters the list after it, a plan
// that the scan and temp B-tree checks pass.

// listProblems returns why the lists of query do not drive plan:
//   - a json_each(?) table that is an inner loop of its join, after a loop of another table
//     (FROM json_each(?) j CROSS JOIN t fixes the order);
//   - an IN list, IN (SELECT ... FROM json_each(?)) or a slice, whose left side (a column or a row
//     value) is in no SEARCH constraint of the plan; on the column's own table when it is
//     qualified, and as rowid when it is an INTEGER PRIMARY KEY (rowids).
func listProblems(query string, plan []step, rowids map[string]bool) []string {
	ts := tokenize(query)
	aliases, ins := listsOf(ts)
	var out []string
	if len(aliases) > 0 {
		out = append(out, listOrder(plan, aliases)...)
	}
	for _, in := range ins {
		if !keyed(in, plan, rowids) {
			out = append(out, fmt.Sprintf("the list of %s IN (...) keys no SEARCH (none takes %s by equality): the plan reads a range by a shorter key and filters the list after it",
				in.text, strings.Join(in.cols, " and ")))
		}
	}
	return out
}

// inList is the left side of an IN list.
type inList struct {
	alias string   // the table of the columns, "" when they are not qualified
	cols  []string // the columns, in lower case
	text  string   // as written
}

// listsOf returns the names that the plan gives the json_each tables of a parameter (their alias,
// or json_each), and the IN lists of ts.
func listsOf(ts []token) (map[string]bool, []inList) {
	aliases := map[string]bool{}
	var ins []inList
	for i := 0; i+1 < len(ts); i++ {
		if ts[i+1].text != "(" {
			continue
		}
		end := closing(ts, i+1)
		switch {
		case ts[i].is("json_each") && param(ts, i+2):
			aliases[aliasAfter(ts, end+1)] = true
		case ts[i].is("IN") && i > 0 && !ts[i-1].is("NOT") && isList(ts[i+2:end]):
			if in, ok := inSide(ts[:i]); ok {
				ins = append(ins, in)
			}
		}
	}
	return aliases, ins
}

// isList reports whether the inside of IN ( ... ) is a list: two or more parameters, or a
// SELECT whose FROM is json_each of a parameter.
func isList(in []token) bool {
	if len(in) == 0 {
		return false
	}
	if in[0].is("SELECT") {
		for k := 1; k+2 < len(in); k++ {
			if in[k].depth == in[0].depth && in[k].is("FROM") {
				return in[k+1].is("json_each") && in[k+2].text == "(" && param(in, k+3)
			}
		}
		return false
	}
	n := 0
	for _, t := range in {
		switch {
		case strings.HasPrefix(t.text, "?"):
			n++
		case t.text != ",":
			return false
		}
	}
	return n >= 2
}

// inSide returns the left side of an IN whose tokens before IN are ts: a column, a qualified
// column or a row value of columns.
func inSide(ts []token) (inList, bool) {
	n := len(ts)
	if n == 0 {
		return inList{}, false
	}
	last := ts[n-1]
	if last.text == ")" {
		open := -1
		for k := n - 2; k >= 0; k-- {
			if ts[k].text == "(" && ts[k].depth == last.depth {
				open = k
				break
			}
		}
		if open < 0 || open > 0 && ts[open-1].ident() && !clauseWords[strings.ToUpper(ts[open-1].text)] { // a function call
			return inList{}, false
		}
		in := inList{}
		var b strings.Builder
		for _, t := range ts[open+1 : n-1] {
			b.WriteString(t.text)
			switch {
			case t.text == ",":
				b.WriteByte(' ')
			case t.text == ".":
				in.cols = in.cols[:len(in.cols)-1] // the qualifier: the column follows it
			case t.ident():
				in.cols = append(in.cols, strings.ToLower(t.text))
			default:
				return inList{}, false
			}
		}
		in.text = "(" + b.String() + ")"
		return in, len(in.cols) > 0
	}
	if !last.ident() {
		return inList{}, false
	}
	in := inList{cols: []string{strings.ToLower(last.text)}, text: last.text}
	if n >= 3 && ts[n-2].text == "." && ts[n-3].ident() {
		in.alias, in.text = ts[n-3].text, ts[n-3].text+"."+last.text
	}
	return in, true
}

// eqRE finds the columns that a SEARCH constraint takes by equality.
var eqRE = regexp.MustCompile(`\b(\w+)=\?`)

// keyed reports whether a SEARCH step of plan takes every column of in by equality, on the
// column's table when in names it.
func keyed(in inList, plan []step, rowids map[string]bool) bool {
	for _, st := range plan {
		rest, ok := strings.CutPrefix(st.detail, "SEARCH ")
		if !ok || in.alias != "" && !strings.HasPrefix(rest, in.alias+" ") {
			continue
		}
		eq := map[string]bool{}
		for _, m := range eqRE.FindAllStringSubmatch(rest, -1) {
			eq[strings.ToLower(m[1])] = true
		}
		all := true
		for _, c := range in.cols {
			if !eq[c] && !(eq["rowid"] && rowids[c]) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// listOrder returns a problem for each json_each table of aliases that a join reads after a loop
// of another table: the steps with one parent are the loops of one join, outer first.
func listOrder(plan []step, aliases map[string]bool) []string {
	var out []string
	first := map[int64]string{} // parent -> the first loop of another table
	for _, st := range plan {
		rest, ok := strings.CutPrefix(st.detail, "SCAN ")
		if !ok {
			rest, ok = strings.CutPrefix(st.detail, "SEARCH ")
		}
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, " ")
		switch {
		case aliases[name] && strings.Contains(rest, "VIRTUAL TABLE"):
			if f := first[st.parent]; f != "" {
				out = append(out, fmt.Sprintf("the list %s is an inner loop of %q: start the join from the list (json_each(...) CROSS JOIN the table)", name, f))
			}
		case first[st.parent] == "" && name != "CONSTANT" && !strings.HasPrefix(name, "("):
			first[st.parent] = st.detail
		}
	}
	return out
}

// rowidColumns returns the names of the columns that are the rowid of their table (an INTEGER
// PRIMARY KEY), which a plan names rowid.
func rowidColumns(ctx context.Context, d *DB) (map[string]bool, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT p.name FROM sqlite_schema m JOIN pragma_table_info(m.name) p
WHERE m.type = 'table' AND p.pk = 1 AND upper(p.type) = 'INTEGER' AND instr(upper(m.sql), 'WITHOUT ROWID') = 0
  AND (SELECT count(*) FROM pragma_table_info(m.name) k WHERE k.pk > 0) = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[strings.ToLower(name)] = true
	}
	return out, rows.Err()
}

// token is a word, a parameter, a literal or a punctuation mark of a query, with the depth of
// parentheses it is at; a "(" or ")" has the depth outside it.
type token struct {
	text  string
	depth int
}

func (t token) is(word string) bool { return strings.EqualFold(t.text, word) }

func (t token) ident() bool {
	c := t.text[0]
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '"'
}

// tokenize splits query into tokens, without its comments.
func tokenize(query string) []token {
	var out []token
	depth := 0
	for i := 0; i < len(query); {
		c := query[i]
		start := i
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
			continue
		case strings.HasPrefix(query[i:], "--"):
			for i < len(query) && query[i] != '\n' {
				i++
			}
			continue
		case strings.HasPrefix(query[i:], "/*"):
			if end := strings.Index(query[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				i = len(query)
			}
			continue
		case c == '\'' || c == '"':
			for i++; i < len(query); i++ {
				if query[i] == c {
					if i+1 < len(query) && query[i+1] == c {
						i++
						continue
					}
					i++
					break
				}
			}
		case c == '?' || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			for i++; i < len(query); i++ {
				d := query[i]
				if !(d == '_' || d >= 'a' && d <= 'z' || d >= 'A' && d <= 'Z' || d >= '0' && d <= '9') {
					break
				}
			}
		default:
			i++
		}
		t := token{text: query[start:i], depth: depth}
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
			t.depth = depth
		}
		out = append(out, t)
	}
	return out
}

// closing returns the index of the ")" that closes the "(" at ts[open], or len(ts).
func closing(ts []token, open int) int {
	for k := open + 1; k < len(ts); k++ {
		if ts[k].text == ")" && ts[k].depth == ts[open].depth {
			return k
		}
	}
	return len(ts)
}

// param reports whether ts[k] starts a parameter: "?", "?N" or CAST(? AS ...).
func param(ts []token, k int) bool {
	if k < len(ts) && strings.HasPrefix(ts[k].text, "?") {
		return true
	}
	return k+2 < len(ts) && ts[k].is("CAST") && ts[k+1].text == "(" && strings.HasPrefix(ts[k+2].text, "?")
}

// aliasAfter returns the name that a plan gives the table function that ends before ts[k]: its
// alias, or json_each.
func aliasAfter(ts []token, k int) string {
	if k < len(ts) && ts[k].is("AS") {
		k++
	}
	if k < len(ts) && ts[k].ident() && !clauseWords[strings.ToUpper(ts[k].text)] {
		return ts[k].text
	}
	return "json_each"
}

// clauseWords are the words that can follow a table of FROM in place of an alias, or come before
// the row value of an IN in place of a function name.
var clauseWords = map[string]bool{
	"WHERE": true, "JOIN": true, "CROSS": true, "INNER": true, "LEFT": true, "RIGHT": true, "FULL": true,
	"NATURAL": true, "OUTER": true, "ON": true, "USING": true, "GROUP": true, "ORDER": true, "LIMIT": true,
	"HAVING": true, "WINDOW": true, "UNION": true, "EXCEPT": true, "INTERSECT": true, "RETURNING": true,
	"AND": true, "OR": true, "NOT": true, "WHEN": true, "THEN": true, "ELSE": true,
}
