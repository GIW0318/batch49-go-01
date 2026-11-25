// Package task maps nightly build artefacts onto a component release timeline
// and decides whether the attributed version falls inside a CVE range.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Basis values recorded on an attribution.
const (
	BasisDeclared       = "declared"
	BasisTime           = "time"
	BasisCommit         = "commit"
	BasisBeforeTimeline = "before_timeline"
	BasisAfterTimeline  = "after_timeline"
)

// Confidence anchors used by the attributor.
const (
	ConfidenceDeclared      = 100
	ConfidenceTime          = 90
	ConfidenceCommit        = 70
	ConfidenceAfterTimeline = 60
	ConfidenceUnlocated     = 10
)

// Reason codes attached to a CVE verdict.
const (
	ReasonInRange       = "in_range"
	ReasonOutsideRanges = "outside_ranges"
	ReasonUnlocated     = "unlocated"
)

// Errors surfaced by the attributor.
var (
	ErrBlankField       = errors.New("component, version and build id are required")
	ErrDuplicateRelease = errors.New("release already registered for this component")
	ErrDuplicateCommit  = errors.New("commit is already bound to a release")
	ErrUnknownComponent = errors.New("unknown component")
	ErrUnknownVersion   = errors.New("unknown release version")
	ErrUnknownCommit    = errors.New("unknown commit for this component")
	ErrTimelineConflict = errors.New("declared version conflicts with the release timeline")
	ErrBadRange         = errors.New("cve range endpoints are invalid")
	ErrUnknownID        = errors.New("unknown attribution id")
)

// Release is one tag on a component timeline.
type Release struct {
	Component  string    `json:"component"`
	Version    string    `json:"version"`
	At         time.Time `json:"at"`
	Prerelease bool      `json:"prerelease"`
	Commit     string    `json:"commit"`
	Seq        int       `json:"seq"`
}

// BuildRef is the artefact handed in by the build system.
type BuildRef struct {
	ID           string    `json:"id"`
	Component    string    `json:"component"`
	Version      string    `json:"version"`
	BuiltAt      time.Time `json:"built_at"`
	Commit       string    `json:"commit"`
	ClockTrusted bool      `json:"clock_trusted"`
}

// Attribution is the located release and the evidence behind it.
type Attribution struct {
	ID         string `json:"id"`
	Component  string `json:"component"`
	Version    string `json:"version"`
	Basis      string `json:"basis"`
	Confidence int    `json:"confidence"`
	Outside    bool   `json:"outside"`
}

// Range is one CVE affected interval. Fixed may stay empty.
type Range struct {
	Introduced string `json:"introduced"`
	Fixed      string `json:"fixed"`
}

// Verdict is the CVE answer for one build.
type Verdict struct {
	ID         string `json:"id"`
	Affected   bool   `json:"affected"`
	Confidence int    `json:"confidence"`
	Basis      string `json:"basis"`
	Introduced string `json:"introduced"`
	Fixed      string `json:"fixed"`
	Reason     string `json:"reason"`
}

// Step is one line of the explanation trail.
type Step struct {
	Ordinal int    `json:"ordinal"`
	Action  string `json:"action"`
	Detail  string `json:"detail"`
}

// BuildVersionAttributor holds the per component timelines and commit index.
type BuildVersionAttributor struct {
}

// NewBuildVersionAttributor builds an empty attributor.
func NewBuildVersionAttributor() *BuildVersionAttributor {
	return &BuildVersionAttributor{}
}

// RegisterRelease appends one tag to a component timeline.
func (a *BuildVersionAttributor) RegisterRelease(component, version string, at time.Time) error {
	return errNotImplemented
}

// BindCommit maps a source commit to an already registered release.
func (a *BuildVersionAttributor) BindCommit(component, version, commit string) error {
	return errNotImplemented
}

// Attribute locates the release a build belongs to.
func (a *BuildVersionAttributor) Attribute(b BuildRef) (Attribution, error) {
	return Attribution{}, errNotImplemented
}

// Affected answers whether the build version lies in any CVE range.
func (a *BuildVersionAttributor) Affected(b BuildRef, ranges []Range) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Timeline returns one component timeline, oldest release first.
func (a *BuildVersionAttributor) Timeline(component string) []Release {
	return nil
}

// Latest returns the newest release of a component.
func (a *BuildVersionAttributor) Latest(component string) (Release, bool) {
	return Release{}, false
}

// Components lists every component with at least one release, sorted.
func (a *BuildVersionAttributor) Components() []string {
	return nil
}

// Lookup reads a stored attribution by build id.
func (a *BuildVersionAttributor) Lookup(id string) (Attribution, bool) {
	return Attribution{}, false
}

// Explain returns the three explanation steps of a stored attribution.
func (a *BuildVersionAttributor) Explain(id string) []Step {
	return nil
}
