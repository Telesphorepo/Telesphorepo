package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// The CLI styles what it prints only on a terminal that takes ANSI escapes: NO_COLOR (any value)
// or TERM=dumb turns it off, FORCE_COLOR or CLICOLOR_FORCE (not "0") turns it on. Without style
// every helper prints the bare text, so a pipe, a CI log and the e2e tests see plain lines.
var (
	styleOut = styled(os.Stdout)
	styleErr = styled(os.Stderr)
)

// styled reports whether the CLI styles what it writes to f.
func styled(f *os.File) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	for _, k := range []string{"FORCE_COLOR", "CLICOLOR_FORCE"} {
		if v, ok := os.LookupEnv(k); ok && v != "0" {
			enableANSI(f)
			return true
		}
	}
	if os.Getenv("TERM") == "dumb" || !isTerminal(f) {
		return false
	}
	return enableANSI(f)
}

// paint is a set of ANSI styles for one stream.
type paint bool

// out and errp style text for stdout and stderr.
var (
	out  = paint(styleOut)
	errp = paint(styleErr)
)

func (p paint) wrap(code, s string) string {
	if !p || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p paint) bold(s string) string   { return p.wrap("1", s) }
func (p paint) dim(s string) string    { return p.wrap("2", s) }
func (p paint) red(s string) string    { return p.wrap("31", s) }
func (p paint) green(s string) string  { return p.wrap("32", s) }
func (p paint) yellow(s string) string { return p.wrap("33", s) }
func (p paint) cyan(s string) string   { return p.wrap("36", s) }

// code marks a command, a path or a value that the person types or copies.
func (p paint) code(s string) string { return p.wrap("36", s) }

// mark is the symbol in front of a styled line; without style it is nothing.
func (p paint) mark(sym, code string) string {
	if !p {
		return ""
	}
	return p.wrap(code, sym) + " "
}

// indent is the indent of a detail line under a marked line; without style it is nothing.
func (p paint) indent() string {
	if !p {
		return ""
	}
	return "  "
}

// The line helpers print one line to stderr, where the CLI talks to the person. The message is
// formatted with fmt.Sprintf.

// title prints the heading of a section with a blank line before it (not before the first one).
func title(format string, a ...any) {
	if bool(errp) && sectionStarted {
		fmt.Fprintln(os.Stderr)
	}
	sectionStarted = true
	fmt.Fprintln(os.Stderr, errp.bold(fmt.Sprintf(format, a...)))
}

var sectionStarted bool

// step prints a thing the CLI does now: "→ Building with Maven".
func step(format string, a ...any) {
	fmt.Fprintln(os.Stderr, errp.mark("→", "36")+fmt.Sprintf(format, a...))
}

// done prints a result: "✓ Published 3 files".
func done(format string, a ...any) {
	fmt.Fprintln(os.Stderr, errp.mark("✓", "32")+fmt.Sprintf(format, a...))
}

// warn prints something the person should know but that does not stop the command.
func warn(format string, a ...any) {
	fmt.Fprintln(os.Stderr, errp.mark("!", "33;1")+fmt.Sprintf(format, a...))
}

// note prints a secondary line: a hint, a path, a detail. It is dim and indented when styled.
func note(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	if errp {
		s = "  " + errp.dim(s)
	}
	fmt.Fprintln(os.Stderr, s)
}

// fail prints a line of a failure that the CLI explains itself, with a red ✗ when styled.
func fail(format string, a ...any) {
	fmt.Fprintln(os.Stderr, errp.mark("✗", "31;1")+fmt.Sprintf(format, a...))
}

// codeLine prints a command or a config line to copy, indented: "  telesphorepo update ...".
func codeLine(s string) {
	fmt.Fprintln(os.Stderr, "  "+errp.code(s))
}

// cmdHint prints what the person runs next: "Publish with: telesphorepo publish". label is the
// text before the command; the command stands out when styled.
func cmdHint(label, cmd string) {
	fmt.Fprintln(os.Stderr, errp.mark("→", "36")+label+errp.wrap("1;36", cmd))
}

// failLine is the line of an error that stops the command: "telesphorepo: ..." When styled it is
// a red ✗ line with the "telesphorepo:" dim, and what the person does about it (the sentences
// after the first, "Run: COMMAND") goes on a "→" line of its own with the command highlighted.
func failLine(w io.Writer, msg string) {
	if errp {
		first, rest, more := strings.Cut(msg, "\n")
		prefix := ""
		if after, ok := strings.CutPrefix(first, "telesphorepo: "); ok {
			prefix, first = errp.dim("telesphorepo: "), after
		}
		head, remedy := first, ""
		if loc := failRemedy.FindStringIndex(first); loc != nil {
			head, remedy = first[:loc[0]+1], first[loc[0]+2:]
		}
		msg = errp.wrap("31;1", "✗") + " " + prefix + errp.red(head)
		if remedy != "" {
			msg += "\n" + errp.mark("→", "36") + highlightCommands(remedy)
		}
		if more {
			msg += "\n" + rest
		}
	}
	fmt.Fprintln(w, msg)
}

// failRemedy finds the end of the first sentence of an error, before a sentence that says what
// to do.
var failRemedy = regexp.MustCompile(`\. [A-Z]`)

// highlightCommands highlights the command after "Run: " and each "telesphorepo COMMAND" in s.
func highlightCommands(s string) string {
	if head, cmd, ok := strings.Cut(s, "Run: "); ok {
		return helpInline.ReplaceAllStringFunc(head, errp.code) + "Run: " + errp.wrap("1;36", cmd)
	}
	return helpInline.ReplaceAllStringFunc(s, errp.code)
}

// The help renderer styles the help texts of main.go without changing a character of them:
// a heading (the first line, or a line after a blank line that ends in ":") is bold, an
// example command is cyan with its "(comment)" dim, the first column of a table is bold (cyan
// for a command of the CLI), and "telesphorepo COMMAND" in prose is cyan.
var (
	helpColumns = regexp.MustCompile(`^(\s+)(\S(?:\S| \S)*)(\s{2,})(\S.*)$`)
	helpComment = regexp.MustCompile(`^(.*?\S)(\s+\([^()]*\))$`)
	helpInline  = regexp.MustCompile(`telesphorepo (?:setup|publish|login|logout|status|update|version|help)(?: <[a-z]+>| [A-Z]+(?:\.\.\.)?)?`)
)

// renderHelp prints the help text to w, styled with p.
func renderHelp(w io.Writer, p paint, text string) {
	if !p {
		fmt.Fprint(w, text)
		return
	}
	var b strings.Builder
	prevBlank := true
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		indented := strings.HasPrefix(line, " ")
		switch {
		case trimmed == "":
		case !indented && (i == 0 || prevBlank && strings.HasSuffix(line, ":")):
			line = p.bold(line)
		case indented && (strings.HasPrefix(trimmed, "telesphorepo ") || strings.HasPrefix(trimmed, "curl ")):
			lead := line[:len(line)-len(strings.TrimLeft(line, " "))]
			cmd, comment := trimmed, ""
			if m := helpComment.FindStringSubmatch(trimmed); m != nil {
				cmd, comment = m[1], m[2]
			}
			line = lead + p.code(cmd) + p.dim(comment)
		case indented:
			if m := helpColumns.FindStringSubmatch(line); m != nil {
				name, _, _ := strings.Cut(m[2], " ")
				left := p.bold(m[2])
				if commands[name] != nil || name == "help" || name == "version" {
					left = p.wrap("1;36", m[2])
				}
				line = m[1] + left + m[3] + m[4]
			}
		default:
			line = helpInline.ReplaceAllStringFunc(line, p.code)
		}
		prevBlank = trimmed == ""
		b.WriteString(line + "\n")
	}
	fmt.Fprint(w, b.String())
}

// fields prints label and value pairs with the values in one column:
//
//	device   7
//	created  2026-10-15
//
// to w, styled with p (labels dim). pairs alternate label, value.
func fields(w io.Writer, p paint, indent string, pairs ...string) {
	width := 0
	for i := 0; i < len(pairs); i += 2 {
		width = max(width, len(pairs[i]))
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		pad := strings.Repeat(" ", width-len(pairs[i]))
		fmt.Fprintf(w, "%s%s%s  %s\n", indent, p.dim(pairs[i]), pad, pairs[i+1])
	}
}
