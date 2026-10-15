package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/server"
)

// The demo dataset of "telesphorepo dev". This file creates the demo users and the repos that
// the format plugins declare (format.DemoData), gives the users access, and has each plugin fill
// its repos (internal/demo). It names no format.

const (
	// demoPendingFile exists in the dev data dir while the demo data is written. A dev start
	// that finds it starts over with a fresh data dir.
	demoPendingFile = "demo-data.pending"
	// demoUsersFile holds one "name<TAB>role<TAB>password" line per demo user.
	demoUsersFile = "demo-users.txt"
)

var errDemoIncomplete = errors.New("the demo data of the last run is incomplete")

// demoUser is a demo account. The dev admin is one too (role "admin"), created by devInit. A
// publisher writes to two neighbouring demo repos; a reader reads every private demo repo.
type demoUser struct {
	name, role, password string
	admin, publisher     bool
	write, read          []string // repo names
	id                   int64
}

func newDemoUsers() []*demoUser {
	return []*demoUser{
		{name: "priya", admin: true},
		{name: "jonas", publisher: true},
		{name: "mei", publisher: true},
		{name: "tomas", publisher: true},
		{name: "lena"},
	}
}

func (u *demoUser) roleText() string {
	switch {
	case u.admin:
		return "admin"
	case len(u.write) > 0:
		return "publisher: " + strings.Join(u.write, ", ")
	case len(u.read) > 0:
		return "reader: " + strings.Join(u.read, ", ")
	}
	return "no access"
}

// demoTarget is one demo repo and the plugin that fills it.
type demoTarget struct {
	plugin format.DemoData
	format string
	def    format.DemoRepo
	repo   *repos.Repo
}

// createDemoData creates the demo users (saved to the demo users file), the demo repos and their
// files.
func createDemoData(ctx context.Context, s *server.Services, adminName string) error {
	began := time.Now()
	var targets []*demoTarget
	for _, f := range format.All() {
		if d, ok := f.(format.DemoData); ok {
			for _, def := range d.DemoRepos() {
				targets = append(targets, &demoTarget{plugin: d, format: f.Name(), def: def})
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}

	admin, err := s.Auth.GetUser(ctx, adminName)
	if err != nil {
		return fmt.Errorf("find dev admin: %w", err)
	}
	users := newDemoUsers()
	publishers := 0
	for _, u := range users {
		switch {
		case u.publisher:
			for _, t := range []*demoTarget{targets[publishers%len(targets)], targets[(publishers+1)%len(targets)]} {
				if !slices.Contains(u.write, t.def.Name) {
					u.write = append(u.write, t.def.Name)
				}
			}
			publishers++
		case !u.admin:
			for _, t := range targets {
				if !t.def.Public {
					u.read = append(u.read, t.def.Name)
				}
			}
		}
		u.password = server.RandomPassword()
		nu, err := s.Auth.CreateUser(ctx, u.name, u.password, u.admin)
		if err != nil {
			return fmt.Errorf("create demo user %s: %w", u.name, err)
		}
		u.id, u.role = nu.ID, u.roleText()
	}
	if err := saveDemoUsers(config.JoinDataDir(s.Config.DataDir, demoUsersFile), users); err != nil {
		return err
	}

	run, err := demo.NewRun(ctx, s.DB, s.Assets, began)
	if err != nil {
		return err
	}
	for _, t := range targets {
		repo, err := s.Repos.Create(ctx, t.def.Name, t.format, t.def.Public, t.def.Description, nil)
		if err != nil {
			run.Close()
			return fmt.Errorf("create demo repo %s: %w", t.def.Name, err)
		}
		t.repo = repo
		writers := []int64{admin.ID}
		for _, u := range users {
			if u.admin {
				writers = append(writers, u.id)
			}
		}
		for _, u := range users {
			for _, name := range u.write {
				if name == repo.Name {
					if _, err := s.Auth.SetPermissions(ctx, []string{u.name}, []string{repo.Name}, auth.Write); err != nil {
						run.Close()
						return fmt.Errorf("grant %s write on %s: %w", u.name, name, err)
					}
					writers = append(writers, u.id)
				}
			}
			for _, name := range u.read {
				if name == repo.Name {
					if _, err := s.Auth.SetPermissions(ctx, []string{u.name}, []string{repo.Name}, auth.Read); err != nil {
						run.Close()
						return fmt.Errorf("grant %s read on %s: %w", u.name, name, err)
					}
				}
			}
		}
		run.SetWriters(repo.ID, writers)
	}

	fmt.Fprintln(os.Stderr, "demo data: writing the demo repos (first run only; telesphorepo dev --empty skips it)")
	stop := make(chan struct{})
	var progress sync.WaitGroup
	progress.Add(1)
	go func() {
		defer progress.Done()
		reportDemo(run, stop)
	}()
	for i, t := range targets {
		if err = t.plugin.Demo(ctx, run, t.repo, demo.Rand(uint64(i+1), 0)); err != nil {
			err = fmt.Errorf("demo repo %s: %w", t.def.Name, err)
			break
		}
	}
	if cerr := run.Close(); err == nil {
		err = cerr
	}
	close(stop)
	progress.Wait()
	if err != nil {
		return fmt.Errorf("demo data: %w", err)
	}
	if _, err := s.DB.W.ExecContext(ctx, `ANALYZE`); err != nil {
		return fmt.Errorf("demo data: analyze: %w", err)
	}
	if err := os.Remove(config.JoinDataDir(s.Config.DataDir, demoPendingFile)); err != nil {
		return err
	}
	done, _ := run.Progress()
	fmt.Fprintf(os.Stderr, "demo data: %s files in %s\n", commas(done), time.Since(began).Round(100*time.Millisecond))
	return nil
}

// reportDemo prints the progress to stderr every few seconds until stop closes.
func reportDemo(run *demo.Run, stop <-chan struct{}) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			done, total := run.Progress()
			fmt.Fprintf(os.Stderr, "demo data: %s / %s files\n", commas(done), commas(total))
		}
	}
}

func saveDemoUsers(name string, users []*demoUser) error {
	var b strings.Builder
	for _, u := range users {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", u.name, u.role, u.password)
	}
	return writePrivate(name, b.String())
}

// loadDemoUsers reads the demo users file; a missing file gives none.
func loadDemoUsers(name string) ([]*demoUser, error) {
	f, err := os.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*demoUser
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) == 3 {
			out = append(out, &demoUser{name: parts[0], role: parts[1], password: parts[2]})
		}
	}
	return out, sc.Err()
}

// commas formats n with thousands separators: 150000 -> "150,000".
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
