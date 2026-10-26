//go:build scale

package e2e

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procGetLogicalProcessorInfoEx = kernel32.NewProc("GetLogicalProcessorInformationEx")
	procGetProcessAffinityMask    = kernel32.NewProc("GetProcessAffinityMask")
	procSetProcessAffinityMask    = kernel32.NewProc("SetProcessAffinityMask")
	procQueryPerformanceCounter   = kernel32.NewProc("QueryPerformanceCounter")
	procQueryPerformanceFrequency = kernel32.NewProc("QueryPerformanceFrequency")
	errNotGroupZero               = errors.New("this process runs outside processor group 0")
	qpcFreq                       = func() int64 {
		var f int64
		procQueryPerformanceFrequency.Call(uintptr(unsafe.Pointer(&f)))
		return f
	}()
)

// scaleNow is a clock in nanoseconds for the request timings. The monotonic clock of Go on
// Windows reads the interrupt time, which moves once per timer tick (15.6 ms by default, 1 ms or 0.5 ms while
// a program raises the timer resolution), too coarse for requests of a fraction of a millisecond; the
// performance counter ticks every 100 ns or finer.
func scaleNow() int64 {
	var c int64
	procQueryPerformanceCounter.Call(uintptr(unsafe.Pointer(&c)))
	return int64(float64(c) * 1e9 / float64(qpcFreq))
}

const (
	relationProcessorCore = 0
	relationCache         = 2
	cacheUnified          = 0
	processSetInformation = 0x0200
	processQueryLimited   = 0x1000
)

// readTopology reads the L3 caches and cores of processor group 0 with
// GetLogicalProcessorInformationEx, and the CPUs of this process with GetProcessAffinityMask.
func readTopology() (topology, error) {
	var top topology
	procMask, err := processMask(syscall.Handle(^uintptr(0))) // the pseudo handle of this process
	if err != nil {
		return top, err
	}
	if procMask == 0 {
		return top, errNotGroupZero
	}
	top.avail = maskCPUs(procMask)
	caches, err := logicalProcessorInfo(relationCache)
	if err != nil {
		return top, err
	}
	// SYSTEM_LOGICAL_PROCESSOR_INFORMATION_EX: Relationship (4), Size (4), then CACHE_RELATIONSHIP:
	// Level (1), Associativity (1), LineSize (2), CacheSize (4), Type (4), Reserved (18),
	// GroupCount (2), GroupMask (GROUP_AFFINITY: Mask (8), Group (2), Reserved (6)).
	for _, rec := range caches {
		level, size, typ := rec[8], binary.LittleEndian.Uint32(rec[12:]), binary.LittleEndian.Uint32(rec[16:])
		mask, group := binary.LittleEndian.Uint64(rec[40:]), binary.LittleEndian.Uint16(rec[48:])
		if level == 3 && typ == cacheUnified && group == 0 {
			top.l3 = append(top.l3, cacheDomain{size: int64(size), cpus: maskCPUs(uintptr(mask))})
		}
	}
	cores, err := logicalProcessorInfo(relationProcessorCore)
	if err != nil {
		return top, err
	}
	// PROCESSOR_RELATIONSHIP: Flags (1), EfficiencyClass (1), Reserved (20), GroupCount (2),
	// GroupMask[0] at offset 24 of the relationship.
	for _, rec := range cores {
		mask, group := binary.LittleEndian.Uint64(rec[32:]), binary.LittleEndian.Uint16(rec[40:])
		if group == 0 {
			top.cores = append(top.cores, maskCPUs(uintptr(mask)))
		}
	}
	return top, nil
}

// logicalProcessorInfo returns the records of GetLogicalProcessorInformationEx for rel.
func logicalProcessorInfo(rel uint32) ([][]byte, error) {
	var n uint32
	procGetLogicalProcessorInfoEx.Call(uintptr(rel), 0, uintptr(unsafe.Pointer(&n)))
	if n == 0 {
		return nil, fmt.Errorf("GetLogicalProcessorInformationEx: no size")
	}
	buf := make([]byte, n)
	if r, _, err := procGetLogicalProcessorInfoEx.Call(uintptr(rel), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return nil, fmt.Errorf("GetLogicalProcessorInformationEx: %w", err)
	}
	var out [][]byte
	for off := uint32(0); off+8 <= n; {
		size := binary.LittleEndian.Uint32(buf[off+4:])
		if size == 0 || off+size > n {
			break
		}
		out = append(out, buf[off:off+size])
		off += size
	}
	return out, nil
}

func processMask(h syscall.Handle) (uintptr, error) {
	var proc, sys uintptr
	if r, _, err := procGetProcessAffinityMask.Call(uintptr(h), uintptr(unsafe.Pointer(&proc)), uintptr(unsafe.Pointer(&sys))); r == 0 {
		return 0, fmt.Errorf("GetProcessAffinityMask: %w", err)
	}
	return proc, nil
}

func setProcessMask(h syscall.Handle, cpus []int) error {
	var mask uintptr
	for _, c := range cpus {
		mask |= 1 << c
	}
	if r, _, err := procSetProcessAffinityMask.Call(uintptr(h), mask); r == 0 {
		return fmt.Errorf("SetProcessAffinityMask: %w", err)
	}
	return nil
}

func maskCPUs(mask uintptr) []int {
	var out []int
	for m := uint64(mask); m != 0; m &= m - 1 {
		out = append(out, bits.TrailingZeros64(m))
	}
	return out
}

// pinSelf pins every thread of this process to cpus.
func pinSelf(cpus []int) error {
	return setProcessMask(syscall.Handle(^uintptr(0)), cpus)
}

// startPinned starts cmd and pins the whole process to cpus. The process starts with the CPUs
// of this one (the client's) and moves before it serves: the server set applies to every thread.
func startPinned(cmd *exec.Cmd, cpus []int) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	h, err := syscall.OpenProcess(processSetInformation|processQueryLimited, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("OpenProcess %d: %w", cmd.Process.Pid, err)
	}
	defer syscall.CloseHandle(h)
	if err := setProcessMask(h, cpus); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	return nil
}

// affinityOf returns the CPUs that process pid may run on.
func affinityOf(pid int) ([]int, error) {
	h, err := syscall.OpenProcess(processQueryLimited, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(h)
	m, err := processMask(h)
	if err != nil {
		return nil, err
	}
	return maskCPUs(m), nil
}
