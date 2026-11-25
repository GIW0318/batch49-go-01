// Package task solves the smallest safe upgrade target for vulnerable installs.
package task

import "errors"

// errNotImplemented marks behaviour that is not built yet.
var errNotImplemented = errors.New("not implemented")

// Reasons attached to a solved target.
const (
	ReasonUpgrade     = "upgrade"
	ReasonAlreadySafe = "already_safe"
	ReasonNoFix       = "no_fix"
)

// Errors returned by the solver.
var (
	ErrBadRange         = errors.New("affected range is invalid")
	ErrBadVersion       = errors.New("version string is invalid")
	ErrUnknownComponent = errors.New("component has no ranges or release list")
	ErrBadInstalled     = errors.New("installed version is invalid")
	ErrNoFix            = errors.New("no safe released version satisfies the constraints")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Range is one CVE affected range. Introduced is inclusive and Fixed is
// exclusive; an empty Fixed means the component is still unfixed.
type Range struct {
	Component  string `json:"component"`
	CVE        string `json:"cve"`
	Introduced string `json:"introduced,omitempty"`
	Fixed      string `json:"fixed,omitempty"`
}

// Installed is one deployed version of a component.
type Installed struct {
	Component string `json:"component"`
	Version   string `json:"version"`
}

// Target is the recommended upgrade of one installed version.
type Target struct {
	Component  string `json:"component"`
	From       string `json:"from"`
	To         string `json:"to"`
	Moved      bool   `json:"moved"`
	CrossMajor bool   `json:"cross_major"`
	Reason     string `json:"reason"`
}

// Risk describes what stays open when no safe released version exists.
type Risk struct {
	Component string   `json:"component"`
	Installed string   `json:"installed"`
	Highest   string   `json:"highest"`
	Residual  []string `json:"residual,omitempty"`
	Reason    string   `json:"reason"`
}

// UpgradeTargetSolver owns the affected ranges, the release lists and the
// declared compatibility limits.
type UpgradeTargetSolver struct {
}

// NewUpgradeTargetSolver builds an empty solver.
func NewUpgradeTargetSolver() *UpgradeTargetSolver {
	return &UpgradeTargetSolver{}
}

// AddRange records one affected range of a component.
func (s *UpgradeTargetSolver) AddRange(r Range) error {
	return errNotImplemented
}

// AddReleased records one published version of a component.
func (s *UpgradeTargetSolver) AddReleased(component, version string) error {
	return errNotImplemented
}

// Solve returns the smallest safe upgrade target of one installed version.
func (s *UpgradeTargetSolver) Solve(installed Installed) (Target, error) {
	return Target{}, errNotImplemented
}

// Candidates lists every usable upgrade version of one installed version.
func (s *UpgradeTargetSolver) Candidates(installed Installed) []string {
	return nil
}

// NoFix reports the residual risk of an installed version without a safe target.
func (s *UpgradeTargetSolver) NoFix(installed Installed) (Risk, bool) {
	return Risk{}, false
}

// Compatibility records the highest version a component may be upgraded to.
func (s *UpgradeTargetSolver) Compatibility(component, maxVersion string) error {
	return errNotImplemented
}

// Components lists the components with recorded ranges or releases.
func (s *UpgradeTargetSolver) Components() []string {
	return nil
}

// Ranges returns a copy of the affected ranges of one component.
func (s *UpgradeTargetSolver) Ranges(component string) []Range {
	return nil
}

// Released returns the published versions of one component, ascending.
func (s *UpgradeTargetSolver) Released(component string) []string {
	return nil
}

// Snapshot serialises ranges, release lists and compatibility limits.
func (s *UpgradeTargetSolver) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *UpgradeTargetSolver) Restore(data []byte) error {
	return errNotImplemented
}
