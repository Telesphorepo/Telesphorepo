package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// interactive reports whether the CLI can ask the person: stdin and stderr are terminals.
func interactive() bool { return isTerminal(os.Stdin) && isTerminal(os.Stderr) }

var stdin = bufio.NewReader(os.Stdin)

// readLine reads one line from stdin without its line end. At the end of the input it returns
// "" and false.
func readLine() (string, bool) {
	s, err := stdin.ReadString('\n')
	if err != nil && s == "" {
		return "", false
	}
	return strings.TrimRight(s, "\r\n"), true
}

// ask prints question on stderr and reads the answer. def is the answer of an empty line and of
// the end of the input.
func ask(question, def string) string {
	fmt.Fprint(os.Stderr, question)
	s, ok := readLine()
	if s = strings.TrimSpace(s); !ok || s == "" {
		return def
	}
	return s
}

// confirm asks a yes or no question. yes is the answer of an empty line: the question ends in
// [Y/n] when yes is true, else in [y/N]. Without a terminal the answer is the default too.
func confirm(question string, yes bool) bool {
	if !interactive() {
		return yes
	}
	hint := " [y/N] "
	if yes {
		hint = " [Y/n] "
	}
	switch strings.ToLower(ask(question+hint, "")) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return yes
}

// choose asks the person to pick one of options by its number and returns its index. Each option
// is a line; the answer of an empty line is def.
func choose(question string, options []string, def int) int {
	fmt.Fprintln(os.Stderr, question)
	for i, o := range options {
		fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, o)
	}
	for {
		s := ask(fmt.Sprintf("Number [%d]: ", def+1), "")
		if s == "" {
			return def
		}
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n >= 1 && n <= len(options) {
			return n - 1
		}
		fmt.Fprintf(os.Stderr, "Enter a number from 1 to %d.\n", len(options))
	}
}

// openBrowser opens url in the default browser. It returns false when this computer has no
// browser to open.
func openBrowser(url string) bool {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", url)
	case os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "":
		cmd = exec.Command("xdg-open", url)
	default:
		return false
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	go cmd.Wait()
	return true
}
