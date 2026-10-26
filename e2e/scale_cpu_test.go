//go:build scale

package e2e

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// The scale zone pins every server process to one set of logical CPUs and the test process (the
// load client) to a disjoint set, so the servers never compete with the client for a core and
// keep the same caches from block to block. Each OS file (scale_cpu_<os>_test.go) gives the
// topology and the pinning calls; macOS has no CPU affinity API, so there both run unpinned.

// cacheDomain is the set of logical CPUs that share one last-level (L3) cache.
type cacheDomain struct {
	size int64 // bytes
	cpus []int
}

// topology is what the OS says about the logical CPUs of this process.
type topology struct {
	avail []int         // logical CPUs this process may run on
	l3    []cacheDomain // the L3 domains
	cores [][]int       // the logical CPUs of each physical core
}

// errNoAffinity is the topology error of an OS without a CPU affinity API.
var errNoAffinity = errors.New("this OS has no CPU affinity API")

// scaleCPUSets returns the logical CPUs of the servers and of the client. The servers get the
// L3 domain with the largest cache (on a CPU with one 3D V-Cache die, that die), or the CPUs of
// E2E_SCALE_CPUS ("0-15" or "0,2,4-7"); the client gets every other CPU of the process. When one
// L3 domain holds every CPU, the client gets the last quarter of its cores. how says where the
// server set comes from.
func scaleCPUSets(top topology) (server, client []int, how string, err error) {
	if env := os.Getenv("E2E_SCALE_CPUS"); env != "" {
		server, err = parseCPUList(env)
		if err != nil {
			return nil, nil, "", fmt.Errorf("E2E_SCALE_CPUS: %w", err)
		}
		for _, c := range server {
			if !slices.Contains(top.avail, c) {
				return nil, nil, "", fmt.Errorf("E2E_SCALE_CPUS names CPU %d, which this process cannot use (it may use %s)", c, formatCPUList(top.avail))
			}
		}
		client = without(top.avail, server)
		if len(client) == 0 {
			return nil, nil, "", errors.New("E2E_SCALE_CPUS names every CPU: the client needs at least one other")
		}
		return server, client, "E2E_SCALE_CPUS", nil
	}
	var best *cacheDomain
	for i := range top.l3 {
		d := &top.l3[i]
		if best == nil || d.size > best.size {
			best = d
		}
	}
	if best == nil {
		return nil, nil, "", errors.New("the OS reports no L3 cache: set E2E_SCALE_CPUS")
	}
	server = intersect(best.cpus, top.avail)
	how = fmt.Sprintf("the L3 domain with the largest cache (%d MiB of %d domains)", best.size>>20, len(top.l3))
	client = without(top.avail, server)
	if len(client) > 0 {
		return server, client, how, nil
	}
	var cores [][]int
	for _, c := range top.cores {
		if c = intersect(c, server); len(c) > 0 {
			cores = append(cores, c)
		}
	}
	if len(cores) < 2 {
		return nil, nil, "", errors.New("one core only: the servers and the client cannot have their own cores")
	}
	n := max(1, len(cores)/4)
	server, client = nil, nil
	for i, c := range cores {
		if i < len(cores)-n {
			server = append(server, c...)
		} else {
			client = append(client, c...)
		}
	}
	slices.Sort(server)
	slices.Sort(client)
	return server, client, how + ", less the last quarter of its cores for the client", nil
}

func intersect(a, b []int) []int {
	var out []int
	for _, x := range a {
		if slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func without(a, b []int) []int {
	var out []int
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// parseCPUList reads a CPU list as Linux writes it: "0-15", "0,2,4-7".
func parseCPUList(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 0 {
			return nil, fmt.Errorf("bad CPU %q in %q", lo, s)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return nil, fmt.Errorf("bad CPU range %q in %q", part, s)
			}
		}
		for c := a; c <= b; c++ {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no CPU in %q", s)
	}
	slices.Sort(out)
	return out, nil
}

// formatCPUList writes cpus as parseCPUList reads them.
func formatCPUList(cpus []int) string {
	var parts []string
	for i := 0; i < len(cpus); {
		j := i
		for j+1 < len(cpus) && cpus[j+1] == cpus[j]+1 {
			j++
		}
		if j == i {
			parts = append(parts, strconv.Itoa(cpus[i]))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", cpus[i], cpus[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}
