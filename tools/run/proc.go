package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// console serialises whole lines from all children onto stdout.
type console struct{ mu sync.Mutex }

var out console

func (c *console) line(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintln(os.Stdout, s)
}

// prefixWriter buffers a child's output and emits it line by line with a prefix
// ("api │ "). Lines for which filter returns true are swallowed.
type prefixWriter struct {
	prefix string
	filter func(line string) bool
	mu     sync.Mutex
	buf    []byte
}

func newPrefixWriter(name, colorCode string, filter func(string) bool) *prefixWriter {
	return &prefixWriter{prefix: color(colorCode, name+" │ "), filter: filter}
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush emits a trailing partial line, if any.
func (w *prefixWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
}

func (w *prefixWriter) emit(l string) {
	l = strings.TrimRight(l, "\r")
	// Terminal UIs (Vite) redraw with \r; keep only the last frame of the line.
	if i := strings.LastIndexByte(l, '\r'); i >= 0 {
		l = l[i+1:]
	}
	if w.filter != nil && w.filter(l) {
		return
	}
	out.line(w.prefix + l)
}

// child is a long-running subprocess with prefixed output that can be stopped gracefully.
type child struct {
	name string
	cmd  *exec.Cmd
	w    *prefixWriter
	done chan struct{}
	err  error // valid after done is closed
}

func startChild(name, colorCode, dir string, env []string, filter func(string) bool, argv ...string) (*child, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	w := newPrefixWriter(name, colorCode, filter)
	cmd.Stdout, cmd.Stderr = w, w
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	c := &child{name: name, cmd: cmd, w: w, done: make(chan struct{})}
	go func() {
		c.err = cmd.Wait()
		w.Flush()
		close(c.done)
	}()
	return c, nil
}

func (c *child) exited() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// stop asks the child (and its process tree) to exit, then kills it after a grace period.
func (c *child) stop() {
	if c == nil || c.exited() {
		return
	}
	interruptTree(c.cmd)
	select {
	case <-c.done:
		return
	case <-time.After(5 * time.Second):
	}
	killTree(c.cmd)
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
	}
}

// runStep runs a short command attached to the terminal (builds, installs).
func runStep(ctx context.Context, dir string, env []string, argv ...string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
