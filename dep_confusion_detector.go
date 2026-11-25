// Package task normalises dependency coordinates and reports dependency
// confusion collisions together with the allow records that suppress them.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Dependency sources.
const (
	SourceInternal = "internal"
	SourcePublic   = "public"
)

// Collision kinds.
const (
	KindNameShadow = "name_shadow"
	KindTyposquat  = "typo_suspect"
)

// Severity buckets of a collision.
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
)

// Evidence verdicts.
const (
	VerdictBlocked = "blocked"
	VerdictAllowed = "allowed"
	VerdictSuspect = "suspect"
)

// Errors returned by the detector.
var (
	ErrBlankCoordinate = errors.New("coordinate name is required")
	ErrBadSource       = errors.New("coordinate source must be internal or public")
	ErrNotFound        = errors.New("collision not found")
	ErrBadUntil        = errors.New("allow deadline must be in the future")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Coordinate is one resolved dependency coordinate.
type Coordinate struct {
	Name      string
	Source    string
	Publisher string
	Version   string
}

// Config tunes the detector.
type Config struct {
	TypoDistance int
	Scope        string
	EscalateTypo bool
	Now          func() time.Time
}

// Member is one coordinate inside a collision class.
type Member struct {
	Name       string
	Normalized string
	Source     string
	Publisher  string
	Version    string
}

// Collision is one tracked conflict class.
type Collision struct {
	ID         string
	Class      string
	Kind       string
	Severity   string
	Blocked    bool
	Suspicious bool
	Allowed    bool
	AllowUntil time.Time
	Members    []Member
	FirstRound int
	LastRound  int
}

// AllowRecord is one time bounded exemption.
type AllowRecord struct {
	CollisionID string
	Until       time.Time
	Scope       string
	Seq         int64
}

// EvidenceRow is one coordinate row of a forensic report.
type EvidenceRow struct {
	Name      string
	Source    string
	Publisher string
	Version   string
}

// Evidence explains one collision class.
type Evidence struct {
	CollisionID string
	Class       string
	Verdict     string
	Rows        []EvidenceRow
	Rounds      int
}

// ConfusionDetector owns the round counter, the collision table and the
// allow records.
type ConfusionDetector struct {
	cfg Config

	mu         sync.Mutex
	rounds     int
	collisions map[string]*Collision
	allows     []AllowRecord
	seq        int64
}

// NewConfusionDetector builds an empty detector.
func NewConfusionDetector(cfg Config) *ConfusionDetector {
	return &ConfusionDetector{
		cfg:        cfg,
		collisions: make(map[string]*Collision),
	}
}

// Normalize folds a raw package name to its stable comparison form.
func (d *ConfusionDetector) Normalize(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(s, "@") {
		if i := strings.IndexByte(s, '/'); i >= 0 {
			s = s[i+1:]
		}
	}
	s = strings.NewReplacer("-", "", "_", "", ".", "").Replace(s)

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if latin, ok := cyrillicHomoglyphs[r]; ok {
			b.WriteRune(latin)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// cyrillicHomoglyphs maps Cyrillic runes that visually resemble Latin ones.
var cyrillicHomoglyphs = map[rune]rune{
	'а': 'a', 'в': 'b', 'е': 'e', 'к': 'k', 'м': 'm', 'н': 'h',
	'о': 'o', 'р': 'p', 'с': 'c', 'т': 't', 'у': 'y', 'х': 'x',
	'ѕ': 's', 'і': 'i', 'ј': 'j', 'һ': 'h', 'ԁ': 'd', 'ӏ': 'l',
	'ү': 'y', 'ԛ': 'q', 'ԝ': 'w', 'ѵ': 'v', 'ѡ': 'w', 'ї': 'i',
	'А': 'a', 'В': 'b', 'Е': 'e', 'К': 'k', 'М': 'm', 'Н': 'h',
	'О': 'o', 'Р': 'p', 'С': 'c', 'Т': 't', 'У': 'y', 'Х': 'x',
	'Ѕ': 's', 'І': 'i', 'Ј': 'j', 'Һ': 'h', 'Ԁ': 'd', 'Ӏ': 'l',
	'Ү': 'y', 'Ԛ': 'q', 'Ԝ': 'w', 'Ѵ': 'v', 'Ѡ': 'w', 'Ї': 'i',
}

// ClassID derives the stable collision class id of a raw name.
func (d *ConfusionDetector) ClassID(name string) string {
	sum := sha256.Sum256([]byte(d.Normalize(name)))
	return fmt.Sprintf("cc-%08x", binary.BigEndian.Uint32(sum[:4]))
}

type memberKey struct {
	source    string
	publisher string
	version   string
}

type coordGroup struct {
	norm         string
	members      []Member
	seen         map[memberKey]struct{}
	hasInternal  bool
	hasPublic    bool
	internalPubs map[string]struct{}
	publicPubs   map[string]struct{}
}

func newCoordGroup(norm string) *coordGroup {
	return &coordGroup{
		norm:         norm,
		seen:         make(map[memberKey]struct{}),
		internalPubs: make(map[string]struct{}),
		publicPubs:   make(map[string]struct{}),
	}
}

func (g *coordGroup) add(m Member) {
	switch m.Source {
	case SourceInternal:
		g.hasInternal = true
		g.internalPubs[m.Publisher] = struct{}{}
	case SourcePublic:
		g.hasPublic = true
		g.publicPubs[m.Publisher] = struct{}{}
	}

	k := memberKey{source: m.Source, publisher: m.Publisher, version: m.Version}
	if _, ok := g.seen[k]; ok {
		return
	}
	g.seen[k] = struct{}{}
	g.members = append(g.members, m)
}

// isNameShadow reports whether the group contains internal and public rows
// whose publishers are not the same.
func (g *coordGroup) isNameShadow() bool {
	if !g.hasInternal || !g.hasPublic {
		return false
	}
	if len(g.internalPubs) == 1 && len(g.publicPubs) == 1 {
		var ip, pp string
		for p := range g.internalPubs {
			ip = p
		}
		for p := range g.publicPubs {
			pp = p
		}
		return ip != pp
	}
	return true
}

func memberLess(a, b Member) bool {
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if a.Publisher != b.Publisher {
		return a.Publisher < b.Publisher
	}
	if a.Version != b.Version {
		return a.Version < b.Version
	}
	return a.Normalized < b.Normalized
}

func sortedMembers(in []Member) []Member {
	out := append([]Member(nil), in...)
	sort.Slice(out, func(i, j int) bool { return memberLess(out[i], out[j]) })
	return out
}

func cloneMembers(in []Member) []Member {
	return append([]Member(nil), in...)
}

func cloneCollision(c Collision) Collision {
	c.Members = cloneMembers(c.Members)
	return c
}

func cloneAllowRecords(in []AllowRecord) []AllowRecord {
	return append([]AllowRecord(nil), in...)
}

func appendUniqueMembers(dst []Member, seen map[memberKey]struct{}, src []Member) []Member {
	for _, m := range src {
		k := memberKey{source: m.Source, publisher: m.Publisher, version: m.Version}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		dst = append(dst, m)
	}
	return dst
}

// Scan normalises the coordinates, groups them and reports collisions.
func (d *ConfusionDetector) Scan(coords []Coordinate) ([]Collision, error) {
	candidates, err := d.buildCollisions(coords)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.collisions == nil {
		d.collisions = make(map[string]*Collision)
	}
	d.rounds++
	round := d.rounds
	now := d.now()

	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	result := make([]Collision, 0, len(ids))
	for _, id := range ids {
		cand := candidates[id]
		if existing, ok := d.collisions[id]; ok {
			if cand.Kind == KindTyposquat && existing.Kind == KindNameShadow {
				existing.LastRound = round
				status := d.applyStatus(*existing, now)
				*existing = status
				result = append(result, cloneCollision(status))
				continue
			}
			existing.Class = cand.Class
			existing.Kind = cand.Kind
			existing.Severity = cand.Severity
			existing.Suspicious = cand.Suspicious
			existing.Members = cloneMembers(cand.Members)
			existing.LastRound = round
			status := d.applyStatus(*existing, now)
			*existing = status
			result = append(result, cloneCollision(status))
			continue
		}

		cand.FirstRound = round
		cand.LastRound = round
		cand.Members = cloneMembers(cand.Members)
		status := d.applyStatus(cand, now)
		stored := status
		d.collisions[id] = &stored
		result = append(result, cloneCollision(status))
	}

	return result, nil
}

// buildCollisions validates and groups one batch without touching detector
// state. It never mutates the detector.
func (d *ConfusionDetector) buildCollisions(coords []Coordinate) (map[string]Collision, error) {
	groups := make(map[string]*coordGroup)
	for _, c := range coords {
		if strings.TrimSpace(c.Name) == "" {
			return nil, ErrBlankCoordinate
		}
		if c.Source != SourceInternal && c.Source != SourcePublic {
			return nil, ErrBadSource
		}
		norm := d.Normalize(c.Name)
		if norm == "" {
			return nil, ErrBlankCoordinate
		}
		g := groups[norm]
		if g == nil {
			g = newCoordGroup(norm)
			groups[norm] = g
		}
		g.add(Member{
			Name:       c.Name,
			Normalized: norm,
			Source:     c.Source,
			Publisher:  c.Publisher,
			Version:    c.Version,
		})
	}

	names := make([]string, 0, len(groups))
	for norm := range groups {
		names = append(names, norm)
	}
	sort.Strings(names)

	candidates := make(map[string]Collision)
	shadowIDs := make(map[string]struct{})

	for _, norm := range names {
		g := groups[norm]
		if !g.isNameShadow() {
			continue
		}
		id := d.ClassID(norm)
		shadowIDs[id] = struct{}{}
		candidates[id] = Collision{
			ID:         id,
			Class:      norm,
			Kind:       KindNameShadow,
			Severity:   SeverityHigh,
			Suspicious: false,
			Members:    sortedMembers(g.members),
		}
	}

	if d.cfg.TypoDistance > 0 {
		adjacent := make(map[string][]string)
		for i := 0; i < len(names); i++ {
			a := groups[names[i]]
			if a.isNameShadow() {
				continue
			}
			for j := i + 1; j < len(names); j++ {
				b := groups[names[j]]
				if b.isNameShadow() {
					continue
				}
				if abs(utf8.RuneCountInString(a.norm)-utf8.RuneCountInString(b.norm)) > d.cfg.TypoDistance {
					continue
				}
				dist := editDistance(a.norm, b.norm)
				if dist == 0 || dist > d.cfg.TypoDistance {
					continue
				}
				adjacent[a.norm] = append(adjacent[a.norm], b.norm)
				adjacent[b.norm] = append(adjacent[b.norm], a.norm)
			}
		}

		visited := make(map[string]bool)
		for _, start := range names {
			if visited[start] || groups[start].isNameShadow() {
				continue
			}
			if len(adjacent[start]) == 0 {
				visited[start] = true
				continue
			}

			component := make([]*coordGroup, 0)
			queue := []string{start}
			visited[start] = true
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				component = append(component, groups[cur])
				for _, next := range adjacent[cur] {
					if visited[next] {
						continue
					}
					visited[next] = true
					queue = append(queue, next)
				}
			}
			if len(component) < 2 {
				continue
			}

			class := typoComponentClass(component)
			id := d.ClassID(class)
			if _, isShadow := shadowIDs[id]; isShadow {
				continue
			}
			seen := make(map[memberKey]struct{})
			members := make([]Member, 0)
			for _, g := range component {
				members = appendUniqueMembers(members, seen, g.members)
			}
			candidates[id] = Collision{
				ID:         id,
				Class:      class,
				Kind:       KindTyposquat,
				Severity:   SeverityMedium,
				Suspicious: true,
				Members:    members,
			}
		}
	}

	for id, cand := range candidates {
		cand.Members = sortedMembers(cand.Members)
		candidates[id] = cand
	}
	return candidates, nil
}

// typoComponentClass picks the internal-side name of a suspicion cluster,
// falling back to the lexicographically smallest name when no side is
// internal.
func typoComponentClass(component []*coordGroup) string {
	best := ""
	for _, g := range component {
		if !g.hasInternal {
			continue
		}
		if best == "" || g.norm < best {
			best = g.norm
		}
	}
	if best != "" {
		return best
	}
	for _, g := range component {
		if best == "" || g.norm < best {
			best = g.norm
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func editDistance(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}

	prev := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur := make([]int, len(br)+1)
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 0
			if ar[i-1] != br[j-1] {
				cost = 1
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func (d *ConfusionDetector) now() time.Time {
	if d.cfg.Now != nil {
		return d.cfg.Now()
	}
	return time.Now()
}

func (d *ConfusionDetector) activeAllowFor(collisionID string, now time.Time) (time.Time, bool) {
	var best time.Time
	found := false
	for i := range d.allows {
		a := d.allows[i]
		if a.CollisionID != collisionID || !a.Until.After(now) {
			continue
		}
		if !found || a.Until.After(best) {
			best = a.Until
			found = true
		}
	}
	return best, found
}

func (d *ConfusionDetector) applyStatus(c Collision, now time.Time) Collision {
	c = cloneCollision(c)
	until, allowed := d.activeAllowFor(c.ID, now)
	if allowed {
		c.Allowed = true
		c.AllowUntil = until
		c.Blocked = false
		return c
	}
	c.Allowed = false
	c.AllowUntil = time.Time{}
	switch c.Kind {
	case KindNameShadow:
		c.Blocked = true
	case KindTyposquat:
		c.Blocked = d.cfg.EscalateTypo
	default:
		c.Blocked = false
	}
	return c
}

// Allow exempts one collision class until the given deadline.
func (d *ConfusionDetector) Allow(collisionID string, until time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.collisions[collisionID]; !ok {
		return ErrNotFound
	}
	now := d.now()
	if !until.After(now) {
		return ErrBadUntil
	}

	kept := d.allows[:0]
	for _, a := range d.allows {
		if a.CollisionID == collisionID && a.Until.After(now) {
			continue
		}
		kept = append(kept, a)
	}
	d.allows = kept

	d.seq++
	d.allows = append(d.allows, AllowRecord{
		CollisionID: collisionID,
		Until:       until,
		Scope:       d.cfg.Scope,
		Seq:         d.seq,
	})

	if c, ok := d.collisions[collisionID]; ok {
		status := d.applyStatus(*c, now)
		*c = status
	}
	return nil
}

// RevokeAllow drops the active exemption of one collision class.
func (d *ConfusionDetector) RevokeAllow(collisionID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	found := false
	kept := make([]AllowRecord, 0, len(d.allows))
	for _, a := range d.allows {
		if a.CollisionID == collisionID && a.Until.After(now) {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return ErrNotFound
	}
	d.allows = kept

	if c, ok := d.collisions[collisionID]; ok {
		status := d.applyStatus(*c, now)
		*c = status
	}
	return nil
}

// ActiveAllows lists the exemptions that are still live at the given instant.
func (d *ConfusionDetector) ActiveAllows(now time.Time) []AllowRecord {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]AllowRecord, 0, len(d.allows))
	for _, a := range d.allows {
		if a.Until.After(now) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CollisionID != out[j].CollisionID {
			return out[i].CollisionID < out[j].CollisionID
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

// Collisions lists the collision classes of the last scan by class id.
func (d *ConfusionDetector) Collisions() []Collision {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	out := make([]Collision, 0, len(d.collisions))
	for _, c := range d.collisions {
		out = append(out, d.applyStatus(*c, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Explain returns the forensic rows behind one collision class.
func (d *ConfusionDetector) Explain(collisionID string) (Evidence, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	c, ok := d.collisions[collisionID]
	if !ok {
		return Evidence{}, ErrNotFound
	}
	collision := d.applyStatus(*c, d.now())

	rows := make([]EvidenceRow, 0, len(collision.Members))
	for _, m := range collision.Members {
		rows = append(rows, EvidenceRow{
			Name:      m.Name,
			Source:    m.Source,
			Publisher: m.Publisher,
			Version:   m.Version,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Source != rows[j].Source {
			return rows[i].Source < rows[j].Source
		}
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		if rows[i].Publisher != rows[j].Publisher {
			return rows[i].Publisher < rows[j].Publisher
		}
		return rows[i].Version < rows[j].Version
	})

	verdict := VerdictSuspect
	if collision.Allowed {
		verdict = VerdictAllowed
	} else if collision.Blocked {
		verdict = VerdictBlocked
	}

	return Evidence{
		CollisionID: collision.ID,
		Class:       collision.Class,
		Verdict:     verdict,
		Rows:        rows,
		Rounds:      d.rounds,
	}, nil
}

// Rounds reports how many scans the detector has completed.
func (d *ConfusionDetector) Rounds() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rounds
}

const snapshotVersion = 1

type snapshotPayload struct {
	Version    int           `json:"version"`
	Rounds     int           `json:"rounds"`
	Collisions []Collision   `json:"collisions"`
	Allows     []AllowRecord `json:"allows"`
	Seq        int64         `json:"seq"`
}

// Snapshot serialises rounds, collisions and allow records.
func (d *ConfusionDetector) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ids := make([]string, 0, len(d.collisions))
	for id := range d.collisions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	p := snapshotPayload{
		Version: snapshotVersion,
		Rounds:  d.rounds,
		Allows:  cloneAllowRecords(d.allows),
		Seq:     d.seq,
	}
	p.Collisions = make([]Collision, 0, len(ids))
	for _, id := range ids {
		p.Collisions = append(p.Collisions, cloneCollision(*d.collisions[id]))
	}
	return json.Marshal(p)
}

// Restore replaces the whole detector state with a snapshot, all or nothing.
func (d *ConfusionDetector) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}

	var p snapshotPayload
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&p); err != nil {
		return ErrBadSnapshot
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return ErrBadSnapshot
	}
	if p.Version != snapshotVersion || p.Rounds < 0 || p.Seq < 0 {
		return ErrBadSnapshot
	}

	collisions := make(map[string]*Collision, len(p.Collisions))
	for _, raw := range p.Collisions {
		c := cloneCollision(raw)
		if err := d.validateSnapshotCollision(c, p.Rounds); err != nil {
			return ErrBadSnapshot
		}
		if _, dup := collisions[c.ID]; dup {
			return ErrBadSnapshot
		}
		collisions[c.ID] = &c
	}

	allows := cloneAllowRecords(p.Allows)
	seenSeq := make(map[int64]struct{}, len(allows))
	var maxSeq int64
	for _, a := range allows {
		if a.CollisionID == "" || a.Seq <= 0 || a.Until.IsZero() {
			return ErrBadSnapshot
		}
		if _, ok := collisions[a.CollisionID]; !ok {
			return ErrBadSnapshot
		}
		if _, dup := seenSeq[a.Seq]; dup {
			return ErrBadSnapshot
		}
		seenSeq[a.Seq] = struct{}{}
		if a.Seq > maxSeq {
			maxSeq = a.Seq
		}
	}
	if p.Seq < maxSeq {
		return ErrBadSnapshot
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.rounds = p.Rounds
	d.collisions = collisions
	d.allows = allows
	d.seq = p.Seq
	return nil
}

func (d *ConfusionDetector) validateSnapshotCollision(c Collision, rounds int) error {
	if len(c.ID) != 11 || !strings.HasPrefix(c.ID, "cc-") || !isLowerHex8(c.ID[3:]) {
		return ErrBadSnapshot
	}
	if c.Class == "" || d.Normalize(c.Class) != c.Class {
		return ErrBadSnapshot
	}
	if d.ClassID(c.Class) != c.ID {
		return ErrBadSnapshot
	}
	switch c.Kind {
	case KindNameShadow:
		if c.Severity != SeverityHigh {
			return ErrBadSnapshot
		}
	case KindTyposquat:
		if c.Severity != SeverityMedium || !c.Suspicious {
			return ErrBadSnapshot
		}
	default:
		return ErrBadSnapshot
	}
	if c.FirstRound < 1 || c.LastRound < c.FirstRound || c.LastRound > rounds {
		return ErrBadSnapshot
	}
	if len(c.Members) == 0 {
		return ErrBadSnapshot
	}
	for _, m := range c.Members {
		if strings.TrimSpace(m.Name) == "" {
			return ErrBadSnapshot
		}
		if m.Normalized == "" || d.Normalize(m.Name) != m.Normalized {
			return ErrBadSnapshot
		}
		if m.Source != SourceInternal && m.Source != SourcePublic {
			return ErrBadSnapshot
		}
	}
	return nil
}

func isLowerHex8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
