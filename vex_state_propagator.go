// Package task propagates upstream VEX statements to downstream consumers,
// orders revisions, and keeps retractions and acknowledgements apart.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// VEX statuses understood by the propagator.
const (
	StatusAffected           = "affected"
	StatusNotAffected        = "not_affected"
	StatusUnderInvestigation = "under_investigation"
	StatusRetracted          = "retracted"
)

// Basis values recorded on a consumer facing state.
const (
	BasisUpstream          = "upstream"
	BasisOutsideRange      = "outside_range"
	BasisRetracted         = "retracted"
	BasisExpired           = "expired"
	BasisUpstreamWithLocal = "upstream_with_local"
)

// Record kinds kept in the per consumer history.
const (
	RecordStatement  = "statement"
	RecordRetraction = "retraction"
)

// Errors surfaced by the propagator.
var (
	ErrBlankField         = errors.New("upstream, component, cve and status are required")
	ErrBadStatus          = errors.New("unknown vex status")
	ErrBadRevision        = errors.New("revision must be at least one")
	ErrBadRange           = errors.New("affected version range is invalid")
	ErrDuplicateStatement = errors.New("statement id already published")
	ErrUnknownConsumer    = errors.New("consumer is not registered")
	ErrUnknownComponent   = errors.New("component is not registered")
	ErrUnknownStatement   = errors.New("no statement for this component and cve")
)

// Statement is one upstream VEX declaration.
type Statement struct {
	ID          string    `json:"id"`
	Upstream    string    `json:"upstream"`
	Component   string    `json:"component"`
	CVE         string    `json:"cve"`
	Status      string    `json:"status"`
	Revision    int       `json:"revision"`
	AffectsFrom string    `json:"affects_from"`
	AffectsTo   string    `json:"affects_to"`
	DeclaredAt  time.Time `json:"declared_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// LocalFinding is a consumer side analysis kept next to the upstream one.
type LocalFinding struct {
	Consumer  string    `json:"consumer"`
	Component string    `json:"component"`
	CVE       string    `json:"cve"`
	Status    string    `json:"status"`
	Revision  int       `json:"revision"`
	At        time.Time `json:"at"`
}

// State is the effective conclusion for one consumer.
type State struct {
	Consumer       string `json:"consumer"`
	Component      string `json:"component"`
	CVE            string `json:"cve"`
	Status         string `json:"status"`
	UpstreamStatus string `json:"upstream_status"`
	LocalStatus    string `json:"local_status"`
	Basis          string `json:"basis"`
	Revision       int    `json:"revision"`
	Acked          bool   `json:"acked"`
}

// Record is one entry of the per consumer conclusion history.
type Record struct {
	Kind     string    `json:"kind"`
	Status   string    `json:"status"`
	Revision int       `json:"revision"`
	At       time.Time `json:"at"`
	Upstream string    `json:"upstream"`
	Valid    bool      `json:"valid"`
	Acked    bool      `json:"acked"`
}

// VexStatePropagator holds consumers, component versions and conclusions.
type VexStatePropagator struct {
}

// NewVexStatePropagator builds an empty propagator.
func NewVexStatePropagator() *VexStatePropagator {
	return &VexStatePropagator{}
}

// RegisterConsumer adds a downstream consumer.
func (p *VexStatePropagator) RegisterConsumer(consumer string) error {
	return errNotImplemented
}

// RegisterComponent records the version a component currently ships.
func (p *VexStatePropagator) RegisterComponent(component, version string) error {
	return errNotImplemented
}

// Publish stores one upstream statement.
func (p *VexStatePropagator) Publish(st Statement, at time.Time) error {
	return errNotImplemented
}

// StateFor resolves the conclusion a consumer should act on.
func (p *VexStatePropagator) StateFor(consumer, component, cve string) (State, error) {
	return State{}, errNotImplemented
}

// Consumers lists the registered consumers following a component and cve.
func (p *VexStatePropagator) Consumers(component, cve string) []string {
	return nil
}

// Ack advances the consumer confirmation watermark.
func (p *VexStatePropagator) Ack(consumer string, upto time.Time) error {
	return errNotImplemented
}

// Retract withdraws the current conclusion for a component and cve.
func (p *VexStatePropagator) Retract(st Statement, at time.Time) error {
	return errNotImplemented
}

// Pending counts the distinct conclusions a consumer has not acknowledged.
func (p *VexStatePropagator) Pending(consumer string) int {
	return 0
}

// History returns the conclusion trail of one component and cve.
func (p *VexStatePropagator) History(consumer, component, cve string) []Record {
	return nil
}

// SubmitLocal records a consumer side analysis as a second source.
func (p *VexStatePropagator) SubmitLocal(f LocalFinding, at time.Time) error {
	return errNotImplemented
}
