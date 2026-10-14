package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	devBinDir = "bin/.dev"
	viteURL   = "http://localhost:5173"
)

// dev runs the backend ("telesphorepo dev") and Vite side by side, rebuilding and
// restarting the backend when Go sources, go.mod/go.sum or migrations change.
func dev(ctx context.Context) error {
	pm, err := prepare(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(devBinDir, 0o755); err != nil {
		return err
	}
	bin := exe(filepath.Join(devBinDir, "telesphorepo"))
	next := exe(filepath.Join(devBinDir, "telesphorepo.next"))

	step("building backend")
	if err := goBuildDev(ctx, next); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("backend build failed (fix it and run again):\n%s", err)
	}
	if err := os.Rename(next, bin); err != nil {
		return err
	}

	b := newBanner(viteURL, viteURL+"/")
	stopBanner := make(chan struct{})
	defer close(stopBanner)

	startAPI := func() (*child, error) {
		// The page is served by Vite, so links, device codes and passkeys use its origin.
		return startChild("api", "35", ".", []string{"TELESPHOREPO_RUNNER=1", "TR_BASE_URL=" + viteURL}, b.filter, bin, "dev")
	}
	api, err := startAPI()
	if err != nil {
		return err
	}
	var webEnv []string
	if useColor {
		webEnv = append(webEnv, "FORCE_COLOR=1")
	}
	web, err := startChild("web", "36", webDir, webEnv, nil, append([]string{pm.Path}, pm.runArgs("dev")...)...)
	if err != nil {
		api.stop()
		return err
	}
	go b.watch(stopBanner)

	stopAll := func() {
		done := make(chan struct{})
		go func() { web.stop(); close(done) }()
		api.stop()
		<-done
	}

	snap := snapshot()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	apiDown := false
	for {
		select {
		case <-ctx.Done():
			out.line("")
			step("stopping")
			stopAll()
			return nil
		case <-web.done:
			api.stop()
			return fmt.Errorf("the web dev server exited (%v); see the web │ lines above", web.err)
		case <-tick.C:
		}
		if !apiDown && api.exited() {
			apiDown = true
			note("api exited (%v); waiting for a change to rebuild", api.err)
		}
		cur := snapshot()
		changed := diff(snap, cur)
		if changed == "" {
			continue
		}
		// Let editors finish multi-file saves before building.
		time.Sleep(200 * time.Millisecond)
		snap = snapshot()
		step("%s changed, rebuilding backend", changed)
		start := time.Now()
		if err := goBuildDev(ctx, next); err != nil {
			if ctx.Err() != nil {
				continue // Ctrl+C during the build; handled at the top of the loop
			}
			out.line(red("✗ build failed") + " — keeping the previous backend running")
			for _, l := range strings.Split(strings.TrimRight(err.Error(), "\n"), "\n") {
				out.line("  " + l)
			}
			continue
		}
		api.stop()
		if err := os.Rename(next, bin); err != nil {
			note("replace binary: %v", err)
			continue
		}
		if api, err = startAPI(); err != nil {
			note("%v", err)
			apiDown = true
			continue
		}
		apiDown = false
		out.line(green("✓") + fmt.Sprintf(" backend restarted (built in %s)", time.Since(start).Round(100*time.Millisecond)))
	}
}

// goBuildDev builds the backend to dst; the error carries the compiler output.
func goBuildDev(ctx context.Context, dst string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", dst, "./cmd/telesphorepo")
	b, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// ---- polling watcher ----

type fileStamp struct {
	mod  time.Time
	size int64
}

var skipDirs = map[string]bool{
	"web": true, ".git": true, "bin": true, "node_modules": true, ".telesphorepo-dev": true,
	".idea": true, ".vscode": true, "data": true, "testdata": true, "tools": true, "e2e": true,
}

// snapshot records *.go, go.mod, go.sum and internal/db/migrations/*.sql under the repo.
func snapshot() map[string]fileStamp {
	m := map[string]fileStamp{}
	_ = filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != "." && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		slash := filepath.ToSlash(p)
		watched := strings.HasSuffix(name, ".go") || slash == "go.mod" || slash == "go.sum" ||
			(path.Dir(slash) == "internal/db/migrations" && strings.HasSuffix(name, ".sql"))
		if !watched {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			m[slash] = fileStamp{fi.ModTime(), fi.Size()}
		}
		return nil
	})
	return m
}

// diff returns a short description of what changed between snapshots ("" if nothing).
func diff(old, cur map[string]fileStamp) string {
	var changed []string
	for p, s := range cur {
		if o, ok := old[p]; !ok || o != s {
			changed = append(changed, p)
		}
	}
	for p := range old {
		if _, ok := cur[p]; !ok {
			changed = append(changed, p)
		}
	}
	switch len(changed) {
	case 0:
		return ""
	case 1:
		return changed[0]
	default:
		return fmt.Sprintf("%s (+%d more)", changed[0], len(changed)-1)
	}
}
