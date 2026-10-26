//go:build scale

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// readTopology reads the L3 caches and cores of /sys/devices/system/cpu, and the CPUs of this
// process with sched_getaffinity.
func readTopology() (topology, error) {
	var top topology
	var set unix.CPUSet
	if err := unix.SchedGetaffinity(0, &set); err != nil {
		return top, fmt.Errorf("sched_getaffinity: %w", err)
	}
	top.avail = setCPUs(&set)
	seenL3, seenCore := map[string]bool{}, map[string]bool{}
	for _, c := range top.avail {
		dir := fmt.Sprintf("/sys/devices/system/cpu/cpu%d", c)
		if s, err := readSys(filepath.Join(dir, "topology", "thread_siblings_list")); err == nil && !seenCore[s] {
			seenCore[s] = true
			if cpus, err := parseCPUList(s); err == nil {
				top.cores = append(top.cores, cpus)
			}
		}
		idx, _ := filepath.Glob(filepath.Join(dir, "cache", "index*"))
		for _, ix := range idx {
			if lvl, _ := readSys(filepath.Join(ix, "level")); lvl != "3" {
				continue
			}
			shared, err := readSys(filepath.Join(ix, "shared_cpu_list"))
			if err != nil || seenL3[shared] {
				continue
			}
			seenL3[shared] = true
			cpus, err := parseCPUList(shared)
			if err != nil {
				return top, err
			}
			size, err := readSys(filepath.Join(ix, "size"))
			if err != nil {
				return top, err
			}
			top.l3 = append(top.l3, cacheDomain{size: parseCacheSize(size), cpus: cpus})
		}
	}
	return top, nil
}

func readSys(name string) (string, error) {
	b, err := os.ReadFile(name)
	return strings.TrimSpace(string(b)), err
}

// parseCacheSize reads a cache size of sysfs ("32768K").
func parseCacheSize(s string) int64 {
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n * mult
}

func setCPUs(set *unix.CPUSet) []int {
	var out []int
	for c := 0; len(out) < set.Count(); c++ { // CPU numbers below CPU_SETSIZE
		if set.IsSet(c) {
			out = append(out, c)
		}
	}
	return out
}

func cpuSet(cpus []int) *unix.CPUSet {
	var set unix.CPUSet
	for _, c := range cpus {
		set.Set(c)
	}
	return &set
}

// pinSelf pins every thread of this process to cpus. A thread that the Go runtime starts later
// inherits the CPUs of the thread that starts it.
func pinSelf(cpus []int) error {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return err
	}
	for _, task := range tasks {
		tid, err := strconv.Atoi(task.Name())
		if err != nil {
			continue
		}
		if err := unix.SchedSetaffinity(tid, cpuSet(cpus)); err != nil {
			return fmt.Errorf("sched_setaffinity %d: %w", tid, err)
		}
	}
	return nil
}

// startPinned starts cmd on cpus: the thread that forks it runs on cpus for the start, and the
// child inherits them before its first instruction.
func startPinned(cmd *exec.Cmd, cpus []int) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var own unix.CPUSet
	if err := unix.SchedGetaffinity(0, &own); err != nil {
		return err
	}
	if err := unix.SchedSetaffinity(0, cpuSet(cpus)); err != nil {
		return fmt.Errorf("sched_setaffinity: %w", err)
	}
	err := cmd.Start()
	if rerr := unix.SchedSetaffinity(0, &own); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

// affinityOf returns the CPUs that process pid may run on.
func affinityOf(pid int) ([]int, error) {
	var set unix.CPUSet
	if err := unix.SchedGetaffinity(pid, &set); err != nil {
		return nil, err
	}
	out := setCPUs(&set)
	slices.Sort(out)
	return out, nil
}

var scaleEpoch = time.Now()

// scaleNow is a clock in nanoseconds for the request timings: the monotonic clock of Go.
func scaleNow() int64 { return int64(time.Since(scaleEpoch)) }
