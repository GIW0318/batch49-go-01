// Package task stores named mixer scenes, ramps parameters between values and
// recalls scenes partially without losing unsaved edits.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// Parameter names accepted by SetParam and Recall.
const (
	ParamVolume = "volume"
	ParamPan    = "pan"
	ParamSend   = "send"
	ParamFX     = "fx"
)

const (
	minRampMS      = 1
	maxRampMS      = 10000
	maxSceneNames  = 32
	snapshotFormat = "mixer-scene-recall-v1"
)

// Errors returned by the scene engine.
var (
	ErrUnknownTrack   = errors.New("unknown track")
	ErrUnknownParam   = errors.New("unknown parameter")
	ErrOutOfRange     = errors.New("parameter value out of range")
	ErrUnknownScene   = errors.New("unknown scene")
	ErrBlankName      = errors.New("scene name is required")
	ErrDuplicateTrack = errors.New("track already registered")
	ErrBadRamp        = errors.New("ramp duration out of range")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Version identifies one saved revision of a scene.
type Version struct {
	Number int    `json:"number"`
	Name   string `json:"name"`
}

// RecallReport describes what one Recall call touched.
type RecallReport struct {
	Scene   string   `json:"scene"`
	Version int      `json:"version"`
	Fields  []string `json:"fields"`
	Jumped  []string `json:"jumped"`
}

// MixerSceneRecall owns the track values, named scenes and running ramps.
type MixerSceneRecall struct {
	mu sync.Mutex
	st *mixerState
}

type mixerState struct {
	RampMs       int                           `json:"ramp_ms"`
	NextVersion  int                           `json:"next_version"`
	Tracks       map[string]*trackState        `json:"tracks"`
	Scenes       map[string]*sceneState        `json:"scenes"`
	SavedTargets map[string]map[string]float64 `json:"saved_targets,omitempty"`
	Reports      map[string]*RecallReport      `json:"reports,omitempty"`
}

type trackState struct {
	Enabled bool                  `json:"enabled"`
	Current map[string]float64    `json:"current"`
	Target  map[string]float64    `json:"target"`
	Dirty   map[string]bool       `json:"dirty,omitempty"`
	Ramps   map[string]*rampState `json:"ramps,omitempty"`
}

type rampState struct {
	RecallKey  string  `json:"recall_key"`
	From       float64 `json:"from"`
	To         float64 `json:"to"`
	DurationMs int     `json:"duration_ms"`
	ElapsedMs  int     `json:"elapsed_ms"`
}

type sceneState struct {
	Name    string                        `json:"name"`
	Version int                           `json:"version"`
	Params  map[string]map[string]float64 `json:"params"`
}

type snapshotEnvelope struct {
	Format string     `json:"format"`
	State  mixerState `json:"state"`
}

// NewMixerSceneRecall builds an empty engine with no tracks and a zero ramp.
func NewMixerSceneRecall() *MixerSceneRecall {
	return &MixerSceneRecall{st: newMixerState()}
}

func newMixerState() *mixerState {
	return &mixerState{
		Tracks:       make(map[string]*trackState),
		Scenes:       make(map[string]*sceneState),
		SavedTargets: make(map[string]map[string]float64),
		Reports:      make(map[string]*RecallReport),
	}
}

func newTrackState() *trackState {
	return &trackState{
		Enabled: true,
		Current: defaultParamValues(),
		Target:  defaultParamValues(),
		Dirty:   make(map[string]bool),
		Ramps:   make(map[string]*rampState),
	}
}

func defaultParamValues() map[string]float64 {
	return map[string]float64{
		ParamVolume: 0,
		ParamPan:    0,
		ParamSend:   0,
		ParamFX:     0,
	}
}

func parameterNames() []string {
	names := []string{ParamVolume, ParamPan, ParamSend, ParamFX}
	sort.Strings(names)
	return names
}

func knownParameter(param string) bool {
	switch param {
	case ParamVolume, ParamPan, ParamSend, ParamFX:
		return true
	default:
		return false
	}
}

func validateParameterValue(param string, value float64) error {
	if err := validateContinuousParameterValue(param, value); err != nil {
		return err
	}
	if param == ParamFX && value != 0 && value != 1 {
		return ErrOutOfRange
	}
	return nil
}

func validateContinuousParameterValue(param string, value float64) error {
	switch param {
	case ParamVolume, ParamSend, ParamFX:
		if math.IsNaN(value) || value < 0 || value > 1 {
			return ErrOutOfRange
		}
	case ParamPan:
		if math.IsNaN(value) || value < -1 || value > 1 {
			return ErrOutOfRange
		}
	default:
		return ErrUnknownParam
	}
	return nil
}

func normalizeFields(fields []string) ([]string, error) {
	selected := make(map[string]struct{})
	if len(fields) == 0 {
		for _, field := range parameterNames() {
			selected[field] = struct{}{}
		}
	} else {
		for _, field := range fields {
			if !knownParameter(field) {
				return nil, fmt.Errorf("%w: %q", ErrUnknownParam, field)
			}
			selected[field] = struct{}{}
		}
	}

	normalized := make([]string, 0, len(selected))
	for field := range selected {
		normalized = append(normalized, field)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func recallKey(scene string, version int, fields []string) string {
	return fmt.Sprintf("%s\x00%d\x00%s", scene, version, strings.Join(fields, "\x00"))
}

func cloneReport(report *RecallReport) RecallReport {
	if report == nil {
		return RecallReport{Fields: []string{}, Jumped: []string{}}
	}
	cloned := *report
	cloned.Fields = append(make([]string, 0, len(report.Fields)), report.Fields...)
	cloned.Jumped = append(make([]string, 0, len(report.Jumped)), report.Jumped...)
	return cloned
}

// AddTrack registers one track id so parameters can be set on it.
func (m *MixerSceneRecall) AddTrack(trackID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.st.Tracks[trackID]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateTrack, trackID)
	}
	m.st.Tracks[trackID] = newTrackState()
	return nil
}

// SetTrackEnabled toggles whether a track follows parameter ramps.
func (m *MixerSceneRecall) SetTrackEnabled(trackID string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	track, ok := m.st.Tracks[trackID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownTrack, trackID)
	}

	track.Enabled = enabled
	if !enabled {
		recallKeys := make(map[string]struct{})
		for _, ramp := range track.Ramps {
			recallKeys[ramp.RecallKey] = struct{}{}
		}

		for param, target := range track.Target {
			track.Current[param] = target
		}
		track.Ramps = make(map[string]*rampState)

		for key := range recallKeys {
			if report := m.st.Reports[key]; report != nil {
				addJumpedTrack(report, trackID)
			}
		}
	}
	return nil
}

func addJumpedTrack(report *RecallReport, trackID string) {
	for _, jumped := range report.Jumped {
		if jumped == trackID {
			return
		}
	}
	report.Jumped = append(report.Jumped, trackID)
	sort.Strings(report.Jumped)
}

// SetRamp sets the ramp length in milliseconds used by later recalls.
func (m *MixerSceneRecall) SetRamp(ms int) error {
	if ms < minRampMS || ms > maxRampMS {
		return fmt.Errorf("%w: %d", ErrBadRamp, ms)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.st.RampMs = ms
	return nil
}

// SetParam writes one parameter target on a registered track.
func (m *MixerSceneRecall) SetParam(trackID, param string, value float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	track, ok := m.st.Tracks[trackID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownTrack, trackID)
	}
	if !knownParameter(param) {
		return fmt.Errorf("%w: %q", ErrUnknownParam, param)
	}
	if err := validateParameterValue(param, value); err != nil {
		return fmt.Errorf("%w: %s=%v", err, param, value)
	}

	track.Current[param] = value
	track.Target[param] = value
	delete(track.Ramps, param)
	track.Dirty[param] = true
	return nil
}

// Value reads the current (possibly interpolated) parameter value.
func (m *MixerSceneRecall) Value(trackID, param string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	track, ok := m.st.Tracks[trackID]
	if !ok {
		return 0
	}
	return track.Current[param]
}

// Save stores the target values of every track under a name.
func (m *MixerSceneRecall) Save(name string) (Version, error) {
	if strings.TrimSpace(name) == "" {
		return Version{}, ErrBlankName
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.st.NextVersion++
	versionNumber := m.st.NextVersion

	sceneParams := make(map[string]map[string]float64, len(m.st.Tracks))
	savedTargets := make(map[string]map[string]float64, len(m.st.Tracks))
	for trackID, track := range m.st.Tracks {
		values := make(map[string]float64, len(parameterNames()))
		savedValues := make(map[string]float64, len(parameterNames()))
		for _, param := range parameterNames() {
			value := track.Target[param]
			values[param] = value
			savedValues[param] = value
		}
		sceneParams[trackID] = values
		savedTargets[trackID] = savedValues
		track.Dirty = make(map[string]bool)
	}

	m.st.Scenes[name] = &sceneState{
		Name:    name,
		Version: versionNumber,
		Params:  sceneParams,
	}
	m.st.SavedTargets = savedTargets
	evictOldestScene(m.st)

	return Version{Number: versionNumber, Name: name}, nil
}

func evictOldestScene(st *mixerState) {
	if len(st.Scenes) <= maxSceneNames {
		return
	}

	oldestName := ""
	oldestVersion := math.MaxInt
	for name, scene := range st.Scenes {
		if scene.Version < oldestVersion || (scene.Version == oldestVersion && name < oldestName) {
			oldestVersion = scene.Version
			oldestName = name
		}
	}
	delete(st.Scenes, oldestName)
}

// Recall restores a saved scene, optionally limited to the listed fields.
func (m *MixerSceneRecall) Recall(name string, fields []string) (RecallReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	scene, ok := m.st.Scenes[name]
	if !ok {
		return RecallReport{}, fmt.Errorf("%w: %q", ErrUnknownScene, name)
	}

	normalizedFields, err := normalizeFields(fields)
	if err != nil {
		return RecallReport{}, err
	}

	key := recallKey(name, scene.Version, normalizedFields)
	if report, ok := activeRecallReport(m.st, key); ok {
		return cloneReport(report), nil
	}

	changes, err := buildRecallChanges(m.st, scene, normalizedFields)
	if err != nil {
		return RecallReport{}, err
	}

	report := &RecallReport{
		Scene:   name,
		Version: scene.Version,
		Fields:  append(make([]string, 0, len(normalizedFields)), normalizedFields...),
		Jumped:  []string{},
	}
	jumped := make(map[string]struct{})

	for _, change := range changes {
		oldValue := change.track.Current[change.param]
		change.track.Target[change.param] = change.target
		savedTrack, saved := m.st.SavedTargets[change.trackID]
		if saved && savedTrack[change.param] == change.target {
			delete(change.track.Dirty, change.param)
		} else {
			change.track.Dirty[change.param] = true
		}

		if !change.track.Enabled || m.st.RampMs <= 0 {
			if oldValue != change.target {
				jumped[change.trackID] = struct{}{}
			}
			change.track.Current[change.param] = change.target
			delete(change.track.Ramps, change.param)
			continue
		}

		if oldValue == change.target {
			change.track.Current[change.param] = change.target
			delete(change.track.Ramps, change.param)
			continue
		}

		change.track.Ramps[change.param] = &rampState{
			RecallKey:  key,
			From:       oldValue,
			To:         change.target,
			DurationMs: m.st.RampMs,
		}
	}

	report.Jumped = sortedSet(jumped)
	storedReport := cloneReport(report)
	m.st.Reports[key] = &storedReport
	return cloneReport(report), nil
}

type recallChange struct {
	trackID string
	track   *trackState
	param   string
	target  float64
}

func buildRecallChanges(st *mixerState, scene *sceneState, fields []string) ([]recallChange, error) {
	trackIDs := make([]string, 0, len(st.Tracks))
	for trackID := range st.Tracks {
		trackIDs = append(trackIDs, trackID)
	}
	sort.Strings(trackIDs)

	changes := make([]recallChange, 0, len(trackIDs)*len(fields))
	for _, trackID := range trackIDs {
		track := st.Tracks[trackID]
		savedParams, exists := scene.Params[trackID]
		if !exists {
			continue
		}

		for _, param := range fields {
			target, ok := savedParams[param]
			if !ok {
				return nil, fmt.Errorf("%w: scene parameter %s/%s is missing", ErrBadSnapshot, trackID, param)
			}
			if err := validateParameterValue(param, target); err != nil {
				return nil, fmt.Errorf("%w: scene parameter %s/%s is invalid", ErrBadSnapshot, trackID, param)
			}
			changes = append(changes, recallChange{
				trackID: trackID,
				track:   track,
				param:   param,
				target:  target,
			})
		}
	}
	return changes, nil
}

func activeRecallReport(st *mixerState, key string) (*RecallReport, bool) {
	active := false
	for _, track := range st.Tracks {
		for _, ramp := range track.Ramps {
			if ramp.RecallKey == key {
				active = true
				break
			}
		}
		if active {
			break
		}
	}
	if !active {
		return nil, false
	}
	report, ok := st.Reports[key]
	if !ok {
		return &RecallReport{Fields: []string{}, Jumped: []string{}}, true
	}
	return report, true
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// Advance moves every running ramp forward by the given milliseconds.
func (m *MixerSceneRecall) Advance(ms int) {
	if ms < 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, track := range m.st.Tracks {
		for param, ramp := range track.Ramps {
			ramp.ElapsedMs += ms
			if ramp.ElapsedMs >= ramp.DurationMs {
				track.Current[param] = ramp.To
				delete(track.Ramps, param)
				continue
			}

			progress := float64(ramp.ElapsedMs) / float64(ramp.DurationMs)
			track.Current[param] = ramp.From + (ramp.To-ramp.From)*progress
		}
	}
}

// Dirty reports whether parameters changed since the last save.
func (m *MixerSceneRecall) Dirty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, track := range m.st.Tracks {
		for _, dirty := range track.Dirty {
			if dirty {
				return true
			}
		}
	}
	return false
}

// Version looks up the latest saved revision of a scene.
func (m *MixerSceneRecall) Version(name string) (Version, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	scene, ok := m.st.Scenes[name]
	if !ok {
		return Version{}, false
	}
	return Version{Number: scene.Version, Name: name}, true
}

// Scenes lists saved scene names in ascending order.
func (m *MixerSceneRecall) Scenes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	names := make([]string, 0, len(m.st.Scenes))
	for name := range m.st.Scenes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Snapshot serialises scenes, versions, targets and running ramps.
func (m *MixerSceneRecall) Snapshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	payload := snapshotEnvelope{
		Format: snapshotFormat,
		State:  *m.st,
	}
	data, err := json.Marshal(&payload)
	if err != nil {
		return nil, fmt.Errorf("encode mixer snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the whole engine state from a snapshot, all or nothing.
func (m *MixerSceneRecall) Restore(data []byte) error {
	state, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.st = state
	m.mu.Unlock()
	return nil
}

func decodeSnapshot(data []byte) (*mixerState, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}

	var payload snapshotEnvelope
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if payload.Format != snapshotFormat {
		return nil, fmt.Errorf("%w: unsupported format", ErrBadSnapshot)
	}

	state := payload.State
	if err := state.validate(); err != nil {
		return nil, err
	}
	return &state, nil
}

func (st *mixerState) validate() error {
	if st.NextVersion < 0 {
		return fmt.Errorf("%w: negative version counter", ErrBadSnapshot)
	}
	if st.RampMs != 0 && (st.RampMs < minRampMS || st.RampMs > maxRampMS) {
		return fmt.Errorf("%w: invalid ramp duration", ErrBadSnapshot)
	}
	if len(st.Scenes) > maxSceneNames {
		return fmt.Errorf("%w: too many scenes", ErrBadSnapshot)
	}

	if st.Tracks == nil {
		st.Tracks = make(map[string]*trackState)
	}
	if st.Scenes == nil {
		st.Scenes = make(map[string]*sceneState)
	}
	if st.Reports == nil {
		st.Reports = make(map[string]*RecallReport)
	}
	if st.SavedTargets == nil {
		st.SavedTargets = make(map[string]map[string]float64)
	}

	activeRecallKeys := make(map[string]struct{})
	for trackID, track := range st.Tracks {
		if track == nil {
			return fmt.Errorf("%w: nil track %q", ErrBadSnapshot, trackID)
		}
		if err := validateSnapshotCurrentValues(track.Current); err != nil {
			return err
		}
		if err := validateSnapshotValues(track.Target); err != nil {
			return err
		}
		if track.Dirty == nil {
			track.Dirty = make(map[string]bool)
		}
		for param := range track.Dirty {
			if !knownParameter(param) {
				return fmt.Errorf("%w: invalid dirty parameter %q", ErrBadSnapshot, param)
			}
		}
		if track.Ramps == nil {
			track.Ramps = make(map[string]*rampState)
		}

		for param, ramp := range track.Ramps {
			if !knownParameter(param) || ramp == nil {
				return fmt.Errorf("%w: invalid ramp for %q/%q", ErrBadSnapshot, trackID, param)
			}
			if ramp.RecallKey == "" {
				return fmt.Errorf("%w: missing ramp recall key", ErrBadSnapshot)
			}
			if err := validateContinuousParameterValue(param, ramp.From); err != nil {
				return fmt.Errorf("%w: invalid ramp origin", ErrBadSnapshot)
			}
			if err := validateParameterValue(param, ramp.To); err != nil {
				return fmt.Errorf("%w: invalid ramp target", ErrBadSnapshot)
			}
			if ramp.DurationMs < minRampMS || ramp.DurationMs > maxRampMS {
				return fmt.Errorf("%w: invalid ramp duration", ErrBadSnapshot)
			}
			if ramp.ElapsedMs < 0 || ramp.ElapsedMs >= ramp.DurationMs {
				return fmt.Errorf("%w: invalid ramp progress", ErrBadSnapshot)
			}
			activeRecallKeys[ramp.RecallKey] = struct{}{}
		}
	}
	for trackID, values := range st.SavedTargets {
		if _, exists := st.Tracks[trackID]; !exists {
			return fmt.Errorf("%w: saved targets have unknown track %q", ErrBadSnapshot, trackID)
		}
		if err := validateSnapshotValues(values); err != nil {
			return err
		}
	}

	sceneVersions := make(map[int]string)
	for name, scene := range st.Scenes {
		if strings.TrimSpace(name) == "" || scene == nil {
			return fmt.Errorf("%w: invalid scene name", ErrBadSnapshot)
		}
		if scene.Version <= 0 || scene.Version > st.NextVersion {
			return fmt.Errorf("%w: invalid scene version", ErrBadSnapshot)
		}
		if previous, exists := sceneVersions[scene.Version]; exists {
			return fmt.Errorf("%w: duplicate scene version %d for %q and %q", ErrBadSnapshot, scene.Version, previous, name)
		}
		sceneVersions[scene.Version] = name
		if scene.Params == nil {
			return fmt.Errorf("%w: missing scene parameters", ErrBadSnapshot)
		}

		for trackID, values := range scene.Params {
			if _, exists := st.Tracks[trackID]; !exists {
				return fmt.Errorf("%w: scene has unknown track %q", ErrBadSnapshot, trackID)
			}
			if err := validateSnapshotValues(values); err != nil {
				return err
			}
		}
	}

	for key := range activeRecallKeys {
		if _, exists := st.Reports[key]; !exists {
			return fmt.Errorf("%w: missing recall report for running ramp", ErrBadSnapshot)
		}
	}
	for key, report := range st.Reports {
		if report == nil {
			return fmt.Errorf("%w: nil recall report", ErrBadSnapshot)
		}
		fields, err := normalizeFields(report.Fields)
		if err != nil {
			return fmt.Errorf("%w: invalid report fields", ErrBadSnapshot)
		}
		report.Fields = fields
		sort.Strings(report.Jumped)
		_ = key
	}

	return nil
}

func validateSnapshotValues(values map[string]float64) error {
	if values == nil || len(values) != len(parameterNames()) {
		return fmt.Errorf("%w: incomplete parameter set", ErrBadSnapshot)
	}
	for _, param := range parameterNames() {
		value, exists := values[param]
		if !exists {
			return fmt.Errorf("%w: missing parameter %q", ErrBadSnapshot, param)
		}
		if err := validateParameterValue(param, value); err != nil {
			return fmt.Errorf("%w: invalid parameter %q", ErrBadSnapshot, param)
		}
	}
	return nil
}

func validateSnapshotCurrentValues(values map[string]float64) error {
	if values == nil || len(values) != len(parameterNames()) {
		return fmt.Errorf("%w: incomplete current parameter set", ErrBadSnapshot)
	}
	for _, param := range parameterNames() {
		value, exists := values[param]
		if !exists {
			return fmt.Errorf("%w: missing current parameter %q", ErrBadSnapshot, param)
		}
		if err := validateContinuousParameterValue(param, value); err != nil {
			return fmt.Errorf("%w: invalid current parameter %q", ErrBadSnapshot, param)
		}
	}
	return nil
}
