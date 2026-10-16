// Package jobs runs the background work of every module under one lifecycle: a job starts
// before the listener opens, its loops run while the server serves, and at shutdown each
// module's work stops and is waited for, in reverse module order, before the database closes.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
)

// Job is one piece of background work of a module. Every part is optional.
type Job struct {
	Name  string                          // "<module>.<name>", unique
	Start func(ctx context.Context) error // runs once before the listener opens; an error stops startup
	Run   func(ctx context.Context)       // a long-lived loop that returns when ctx ends
	Every time.Duration                   // the period of Tick
	Tick  func(ctx context.Context) error // runs when the loops start and then every period; an error is logged
}

// Runner owns one context and one wait group per module. A module's jobs and its on-demand
// tasks (Go) run under that context and stop with it.
type Runner struct {
	log *slog.Logger

	mu      sync.Mutex
	order   []*group
	modules map[string]*group
	names   map[string]bool
}

type group struct {
	module string
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	jobs   []Job

	mu      sync.Mutex
	stopped bool // Stop has waited for every goroutine; Go refuses new work
}

// NewRunner returns a runner for the modules in start order.
func NewRunner(log *slog.Logger, modules []string) *Runner {
	r := &Runner{log: log, modules: map[string]*group{}, names: map[string]bool{}}
	for _, m := range modules {
		// A job records audit events as the system actor of its module.
		ctx, cancel := context.WithCancel(audit.System(context.Background(), m))
		g := &group{module: m, ctx: ctx, cancel: cancel}
		r.order = append(r.order, g)
		r.modules[m] = g
	}
	return r
}

func (r *Runner) group(module string) *group {
	g := r.modules[module]
	if g == nil {
		panic(fmt.Sprintf("jobs: unknown module %q", module))
	}
	return g
}

// Add registers a job of module. It panics on a name that is taken or a Tick without Every.
func (r *Runner) Add(module string, j Job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j.Name == "" || r.names[j.Name] {
		panic(fmt.Sprintf("jobs: job name %q is empty or taken", j.Name))
	}
	if j.Tick != nil && j.Every <= 0 {
		panic(fmt.Sprintf("jobs: job %s has a Tick without Every", j.Name))
	}
	r.names[j.Name] = true
	g := r.group(module)
	g.jobs = append(g.jobs, j)
}

// Context returns the context of module. It ends when the module stops.
func (r *Runner) Context(module string) context.Context { return r.group(module).ctx }

// Go runs fn as an on-demand task of module (a storage move, a Nexus sync, an index rebuild).
// The task gets the module's context and is waited for when the module stops. A task that a
// stopping module starts still runs, with an ended context, so it can release what it holds.
// After the module stopped, Go logs an error and runs nothing.
func (r *Runner) Go(module, name string, fn func(ctx context.Context)) {
	g := r.group(module)
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		r.log.Error("a task started after its module stopped", "module", module, "task", name)
		return
	}
	g.wg.Add(1)
	g.mu.Unlock()
	go func() {
		defer g.wg.Done()
		fn(g.ctx)
	}()
}

// Start runs the Start of every job, in module order. When one fails, the runner stops what
// started and returns the error.
func (r *Runner) Start() error {
	for _, g := range r.order {
		for _, j := range g.jobs {
			if j.Start == nil {
				continue
			}
			if err := j.Start(g.ctx); err != nil {
				r.Stop()
				return fmt.Errorf("start %s: %w", j.Name, err)
			}
		}
	}
	return nil
}

// Loop starts the Run and Tick loops of every job.
func (r *Runner) Loop() {
	for _, g := range r.order {
		for _, j := range g.jobs {
			if j.Run != nil {
				r.Go(g.module, j.Name, j.Run)
			}
			if j.Tick != nil {
				r.Go(g.module, j.Name, func(ctx context.Context) { r.tick(ctx, j) })
			}
		}
	}
}

func (r *Runner) tick(ctx context.Context, j Job) {
	t := time.NewTicker(j.Every)
	defer t.Stop()
	for {
		if err := j.Tick(ctx); err != nil && ctx.Err() == nil {
			r.log.Error("job failed", "job", j.Name, "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Stop ends every module in reverse order: it cancels the module's context and waits for all
// its jobs and tasks. A second call does nothing.
func (r *Runner) Stop() {
	for i := len(r.order) - 1; i >= 0; i-- {
		g := r.order[i]
		g.cancel()
		g.wg.Wait()
		g.mu.Lock()
		g.stopped = true
		g.mu.Unlock()
		g.wg.Wait() // a task that Go added before stopped was set
	}
}
