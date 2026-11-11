// Package e2erun holds the rules of an e2e run that "go tool task test" enforces and the e2e
// TestMain checks: the time budget of a zone's tests, scaled to the speed of the computer, the
// cooldown between the runs of a checkout, and the ticket of the run in progress, which only
// "go tool task test" hands out. The scale and macOS zones are not e2e runs: none of this
// applies to them.
package e2erun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

const (
	// ReferenceBudget is the time the tests of one zone may take on the reference computer, CI's
	// e2e runner, from the first test to the last; the setup before them (the image build, the
	// pulls) does not count. It keeps the suite small, lean and focused: a change that does not fit
	// makes its tests leaner, it never raises ReferenceBudget.
	ReferenceBudget = 4 * time.Minute

	// MaxRun is the most a whole run may take from its first zone on: the zones run one after
	// another, and each zone's budget is at most what is left of MaxRun. It holds on any computer,
	// however slow its calibration.
	MaxRun = 10 * time.Minute

	// ReferenceCalibration is the time of one calibration batch of "go tool task test" (a fixed
	// piece of the work of the tests) on the reference computer, CI's e2e runner, which prints it
	// at the start of each zone. A computer whose calibration takes longer gets a longer budget, a
	// faster one a shorter (Budget).
	ReferenceCalibration = 3 * time.Second

	// The budget of a computer stays within these multiples of ReferenceBudget, so a disturbed
	// calibration cannot make it absurd.
	minBudgetScale, maxBudgetScale = 0.5, 3.0

	// Cooldown is the least time between the end of a run on this computer and the start of the
	// next, from any checkout or agent, so that work is batched: finish a change, then test it
	// once. A run started during the cooldown is refused and starts it again, so waiting it out
	// with sleep and retrying never gets through sooner than working on the next change.
	Cooldown = 3 * time.Minute

	// TicketEnv is the variable that carries the ticket of the run to the test binaries.
	TicketEnv = "E2E_RUN_TICKET"

	// Image is the server image that "go tool task test" builds for the run (E2E_IMAGE names
	// another one and skips the build).
	Image = "telesphorepo-e2e:latest"

	// DataVolumePrefix is the prefix of the data volumes that the harness names with RandName.
	DataVolumePrefix = "telesphorepo-data"

	// RandNameBytes is the number of random bytes of the harness's RandName.
	RandNameBytes = 4
)

// Budget is the budget of the tests of one zone on a computer whose calibration took
// calibration: ReferenceBudget scaled by calibration / ReferenceCalibration, within 0.5x to 3x.
func Budget(calibration time.Duration) time.Duration {
	scale := float64(calibration) / float64(ReferenceCalibration)
	scale = min(max(scale, minBudgetScale), maxBudgetScale)
	return time.Duration(float64(ReferenceBudget) * scale).Round(time.Second)
}

// LeftoverVolume matches the volumes the harness names with RandName and DataVolumePrefix. The
// bare "telesphorepo-data" volume of a person's own server never matches.
var LeftoverVolume = regexp.MustCompile(`^` + regexp.QuoteMeta(DataVolumePrefix) + `-[0-9a-f]{` + strconv.Itoa(2*RandNameBytes) + `}$`)

// Running is the run in progress on this computer: one at a time.
type Running struct {
	PID         int           `json:"pid"`
	Root        string        `json:"root"`
	Ticket      string        `json:"ticket"`
	Started     time.Time     `json:"started"`
	Calibration time.Duration `json:"calibration"`
	Budget      time.Duration `json:"budget"` // of the tests of each zone of the run
}

// Dir is the folder of the state of the runs of this computer.
func Dir() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "telesphorepo", "e2e"), nil
}

// RunningFile is the file of the run in progress.
func RunningFile() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "running.json"), nil
}

// ReadRunning reads the run in progress; os.ErrNotExist when none runs.
func ReadRunning() (Running, error) {
	var r Running
	f, err := RunningFile()
	if err != nil {
		return r, err
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %w", f, err)
	}
	return r, nil
}

// ErrNoTicket is the refusal of a test binary that "go tool task test" did not start.
var ErrNoTicket = fmt.Errorf("start the e2e tests with \"go tool task test\" (see AGENTS.md): it builds the image once, "+
	"holds the tests of each zone to a budget (%s on CI's runner, %s for a whole run) and keeps %s between the runs on this computer", ReferenceBudget, MaxRun, Cooldown)

// CheckTicket returns the run in progress when ticket is its ticket.
func CheckTicket(ticket string) (Running, error) {
	if ticket == "" {
		return Running{}, ErrNoTicket
	}
	r, err := ReadRunning()
	if errors.Is(err, os.ErrNotExist) || (err == nil && (r.Ticket != ticket || r.Budget <= 0)) {
		return Running{}, ErrNoTicket
	}
	return r, err
}
