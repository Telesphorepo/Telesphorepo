//go:build scale && !windows && !linux

package e2e

import (
	"os/exec"
	"time"
)

// macOS (and any OS but Windows and Linux) has no CPU affinity API: the servers and the client
// run where the scheduler puts them.

func readTopology() (topology, error) { return topology{}, errNoAffinity }

func pinSelf([]int) error { return errNoAffinity }

func startPinned(cmd *exec.Cmd, _ []int) error { return cmd.Start() }

func affinityOf(int) ([]int, error) { return nil, errNoAffinity }

var scaleEpoch = time.Now()

// scaleNow is a clock in nanoseconds for the request timings: the monotonic clock of Go.
func scaleNow() int64 { return int64(time.Since(scaleEpoch)) }
