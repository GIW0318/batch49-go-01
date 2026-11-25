// Package task indexes an ever growing fuzzing corpus, reports near duplicates
// by banded fingerprints and keeps the index bounded.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Verdict is the outcome of adding one sample to the corpus.
type Verdict string

// Verdict values returned by Add.
const (
	VerdictNew       Verdict = "new"
	VerdictDuplicate Verdict = "duplicate"
)

// Default tuning values applied when Config leaves a field at its zero value.
const (
	DefaultBandSize     = 8
	DefaultThreshold    = 0.7
	DefaultMaxPerBucket = 64
	DefaultTTL          = 24 * time.Hour
)

// Errors returned by the deduper.
var (
	ErrEmptySample = errors.New("sample id and data are required")
	ErrBadConfig   = errors.New("config values are invalid")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Sample is one corpus entry pushed by the fuzzer.
type Sample struct {
	ID      string    `json:"id"`
	Data    string    `json:"data"`
	AddedAt time.Time `json:"added_at"`
}

// Config tunes banding, duplicate threshold, per bucket capacity and the TTL
// of the fingerprint index.
type Config struct {
	BandSize     int           `json:"band_size"`
	Threshold    float64       `json:"threshold"`
	MaxPerBucket int           `json:"max_per_bucket"`
	TTL          time.Duration `json:"ttl"`
}

// Fingerprint is the banded fingerprint of one sample plus the bucket name it
// is indexed under.
type Fingerprint struct {
	Bands  []uint64 `json:"bands"`
	Bucket string   `json:"bucket"`
}

type indexEntry struct {
	id     string
	bucket string
	bands  []uint64
	seq    uint64
}

// CorpusDeduper owns the sample store and the bounded fingerprint index.
type CorpusDeduper struct {
	mu      sync.Mutex
	cfg     Config
	samples map[string]Sample
	index   map[string]*indexEntry
	buckets map[string][]*indexEntry
	nextSeq uint64
}

// NewCorpusDeduper builds a deduper, filling unset config fields with the
// Default* constants.
func NewCorpusDeduper(cfg Config) *CorpusDeduper {
	cfg = normalizeConfig(cfg)
	return &CorpusDeduper{
		cfg:     cfg,
		samples: make(map[string]Sample),
		index:   make(map[string]*indexEntry),
		buckets: make(map[string][]*indexEntry),
	}
}

func normalizeConfig(cfg Config) Config {
	if cfg.BandSize <= 0 {
		cfg.BandSize = DefaultBandSize
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = DefaultThreshold
	}
	if cfg.MaxPerBucket <= 0 {
		cfg.MaxPerBucket = DefaultMaxPerBucket
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	return cfg
}

func validateSample(s Sample) error {
	if s.ID == "" || s.Data == "" {
		return ErrEmptySample
	}
	return nil
}

func fnv1a64(data []byte) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var hash uint64 = offset64
	for _, b := range data {
		hash ^= uint64(b)
		hash *= prime64
	}
	return hash
}

func bandFingerprint(data string, bandSize int) []uint64 {
	raw := []byte(data)
	bands := make([]uint64, 0, (len(raw)+bandSize-1)/bandSize)
	for len(raw) > 0 {
		n := bandSize
		if n > len(raw) {
			n = len(raw)
		}
		bands = append(bands, fnv1a64(raw[:n]))
		raw = raw[n:]
	}
	return bands
}

func bucketName(bands []uint64) string {
	return fmt.Sprintf("band0-%016x", bands[0])
}

func fingerprint(s Sample, bandSize int) (Fingerprint, error) {
	if err := validateSample(s); err != nil {
		return Fingerprint{}, err
	}
	bands := bandFingerprint(s.Data, bandSize)
	return Fingerprint{Bands: bands, Bucket: bucketName(bands)}, nil
}

func (d *CorpusDeduper) fingerprintLocked(s Sample) (Fingerprint, error) {
	return fingerprint(s, d.cfg.BandSize)
}

// Add ingests one sample and returns the verdict together with the current
// representative sample ID of its bucket.
func (d *CorpusDeduper) Add(c Sample) (Verdict, string, error) {
	if err := validateSample(c); err != nil {
		return "", "", err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.samples[c.ID]; ok {
		if entry, live := d.index[c.ID]; live {
			rep, found := d.representativeLocked(entry.bucket)
			if found {
				return VerdictDuplicate, rep, nil
			}
		}
		return VerdictDuplicate, "", nil
	}

	fp, err := d.fingerprintLocked(c)
	if err != nil {
		return "", "", err
	}

	for _, entry := range d.buckets[fp.Bucket] {
		if similarityBands(fp.Bands, entry.bands) >= d.cfg.Threshold {
			rep, found := d.representativeLocked(fp.Bucket)
			if !found {
				return VerdictDuplicate, "", nil
			}
			return VerdictDuplicate, rep, nil
		}
	}

	d.samples[c.ID] = c
	entry := &indexEntry{
		id:     c.ID,
		bucket: fp.Bucket,
		bands:  append([]uint64(nil), fp.Bands...),
		seq:    d.nextSeq,
	}
	d.nextSeq++
	d.index[c.ID] = entry
	d.buckets[fp.Bucket] = append(d.buckets[fp.Bucket], entry)

	rep, found := d.representativeLocked(fp.Bucket)
	if !found {
		return VerdictNew, c.ID, nil
	}
	return VerdictNew, rep, nil
}

// Similar estimates the similarity of two samples in [0,1] from their banded
// fingerprints.
func (d *CorpusDeduper) Similar(a, b Sample) (float64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	fa, err := d.fingerprintLocked(a)
	if err != nil {
		return 0, err
	}
	fb, err := d.fingerprintLocked(b)
	if err != nil {
		return 0, err
	}
	return similarityBands(fa.Bands, fb.Bands), nil
}

func uniqueBands(bands []uint64) map[uint64]struct{} {
	set := make(map[uint64]struct{}, len(bands))
	for _, band := range bands {
		set[band] = struct{}{}
	}
	return set
}

func similarityBands(a, b []uint64) float64 {
	setA := uniqueBands(a)
	setB := uniqueBands(b)

	longer := len(setA)
	if len(setB) > longer {
		longer = len(setB)
	}
	if longer == 0 {
		return 1
	}

	if len(setA) > len(setB) {
		setA, setB = setB, setA
	}
	shared := 0
	for band := range setA {
		if _, ok := setB[band]; ok {
			shared++
		}
	}
	return float64(shared) / float64(longer)
}

// FingerprintOf computes the banded fingerprint and bucket of a sample.
func (d *CorpusDeduper) FingerprintOf(s Sample) (Fingerprint, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fingerprintLocked(s)
}

// BucketOf reports the bucket a stored sample is currently indexed under.
func (d *CorpusDeduper) BucketOf(id string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, ok := d.index[id]
	if !ok {
		return "", false
	}
	return entry.bucket, true
}

// Representative returns the representative sample ID of one bucket.
func (d *CorpusDeduper) Representative(bucket string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.representativeLocked(bucket)
}

func (d *CorpusDeduper) representativeLocked(bucket string) (string, bool) {
	entries := d.buckets[bucket]
	if len(entries) == 0 {
		return "", false
	}

	var best *indexEntry
	var bestFeatures, bestLen int
	var bestDigest string
	for _, entry := range entries {
		sample, ok := d.samples[entry.id]
		if !ok {
			continue
		}
		features := len(uniqueBands(entry.bands))
		dataLen := len(sample.Data)
		digest := digestData(sample.Data)

		if best == nil || features > bestFeatures ||
			(features == bestFeatures && dataLen > bestLen) ||
			(features == bestFeatures && dataLen == bestLen && digest < bestDigest) ||
			(features == bestFeatures && dataLen == bestLen && digest == bestDigest && entry.id < best.id) {
			best = entry
			bestFeatures = features
			bestLen = dataLen
			bestDigest = digest
		}
	}
	if best == nil {
		return "", false
	}
	return best.id, true
}

func digestData(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// Buckets lists the known bucket names in ascending order.
func (d *CorpusDeduper) Buckets() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	names := make([]string, 0, len(d.buckets))
	for name, entries := range d.buckets {
		if len(entries) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Evict drops expired fingerprints and over capacity fingerprints, oldest
// first, and reports how many index entries were removed.
func (d *CorpusDeduper) Evict(now time.Time) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := now.Add(-d.cfg.TTL)
	removed := 0
	for bucket, entries := range d.buckets {
		live := make([]*indexEntry, 0, len(entries))
		for _, entry := range entries {
			sample, ok := d.samples[entry.id]
			if !ok || sample.AddedAt.Before(cutoff) {
				delete(d.index, entry.id)
				removed++
				continue
			}
			live = append(live, entry)
		}

		if len(live) > d.cfg.MaxPerBucket {
			sort.Slice(live, func(i, j int) bool {
				si := d.samples[live[i].id]
				sj := d.samples[live[j].id]
				if !si.AddedAt.Equal(sj.AddedAt) {
					return si.AddedAt.Before(sj.AddedAt)
				}
				return live[i].id < live[j].id
			})
			over := len(live) - d.cfg.MaxPerBucket
			for _, entry := range live[:over] {
				delete(d.index, entry.id)
				removed++
			}
			live = live[over:]
		}

		if len(live) == 0 {
			delete(d.buckets, bucket)
		} else {
			d.buckets[bucket] = live
		}
	}
	return removed
}

// Lookup returns a stored sample even after its fingerprint was evicted.
func (d *CorpusDeduper) Lookup(id string) (Sample, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sample, ok := d.samples[id]
	return sample, ok
}

// Count reports how many samples are stored.
func (d *CorpusDeduper) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.samples)
}

// FeatureCount reports how many distinct band hashes a sample carries.
func (d *CorpusDeduper) FeatureCount(s Sample) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fp, err := d.fingerprintLocked(s)
	if err != nil {
		return 0, err
	}
	return len(uniqueBands(fp.Bands)), nil
}

// Digest returns the content digest used as the final representative
// tie-breaker.
func (d *CorpusDeduper) Digest(s Sample) string {
	return digestData(s.Data)
}

const (
	snapshotMagic   = "gsb-corpus-deduper"
	snapshotVersion = 1
)

type snapshotEntry struct {
	ID     string   `json:"id"`
	Bucket string   `json:"bucket"`
	Bands  []uint64 `json:"bands"`
	Seq    uint64   `json:"seq"`
}

type snapshotData struct {
	Magic   string          `json:"magic"`
	Version int             `json:"version"`
	Config  Config          `json:"config"`
	NextSeq uint64          `json:"next_seq"`
	Samples []Sample        `json:"samples"`
	Entries []snapshotEntry `json:"entries"`
}

type snapshotEnvelope struct {
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

// Snapshot serialises the sample store and the fingerprint index.
func (d *CorpusDeduper) Snapshot() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ids := make([]string, 0, len(d.samples))
	for id := range d.samples {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	samples := make([]Sample, 0, len(ids))
	for _, id := range ids {
		samples = append(samples, d.samples[id])
	}

	entries := make([]snapshotEntry, 0, len(d.index))
	for _, entry := range d.index {
		entries = append(entries, snapshotEntry{
			ID:     entry.id,
			Bucket: entry.bucket,
			Bands:  append([]uint64(nil), entry.bands...),
			Seq:    entry.seq,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Seq != entries[j].Seq {
			return entries[i].Seq < entries[j].Seq
		}
		return entries[i].ID < entries[j].ID
	})

	payload := snapshotData{
		Magic:   snapshotMagic,
		Version: snapshotVersion,
		Config:  d.cfg,
		NextSeq: d.nextSeq,
		Samples: samples,
		Entries: entries,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(raw)
	envelope, err := json.Marshal(snapshotEnvelope{
		Payload:  raw,
		Checksum: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return nil, err
	}
	return envelope, nil
}

// Restore replaces the store from a snapshot. A corrupt payload leaves the
// deduper untouched.
func (d *CorpusDeduper) Restore(data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	payload, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	samples := make(map[string]Sample, len(payload.Samples))
	for _, sample := range payload.Samples {
		if err := validateSample(sample); err != nil {
			return ErrBadSnapshot
		}
		if _, exists := samples[sample.ID]; exists {
			return ErrBadSnapshot
		}
		samples[sample.ID] = sample
	}

	sortedEntries := append([]snapshotEntry(nil), payload.Entries...)
	sort.Slice(sortedEntries, func(i, j int) bool {
		if sortedEntries[i].Seq != sortedEntries[j].Seq {
			return sortedEntries[i].Seq < sortedEntries[j].Seq
		}
		return sortedEntries[i].ID < sortedEntries[j].ID
	})

	index := make(map[string]*indexEntry, len(sortedEntries))
	buckets := make(map[string][]*indexEntry)
	var maxSeq uint64
	haveSeq := false
	for _, snapEntry := range sortedEntries {
		sample, ok := samples[snapEntry.ID]
		if !ok || snapEntry.ID == "" || len(snapEntry.Bands) == 0 {
			return ErrBadSnapshot
		}
		if _, exists := index[snapEntry.ID]; exists {
			return ErrBadSnapshot
		}
		if haveSeq && snapEntry.Seq == maxSeq {
			return ErrBadSnapshot
		}
		if !haveSeq || snapEntry.Seq > maxSeq {
			maxSeq = snapEntry.Seq
		}
		haveSeq = true

		fp, fpErr := fingerprint(sample, payload.Config.BandSize)
		if fpErr != nil || fp.Bucket != snapEntry.Bucket || !equalBands(fp.Bands, snapEntry.Bands) {
			return ErrBadSnapshot
		}

		entry := &indexEntry{
			id:     snapEntry.ID,
			bucket: snapEntry.Bucket,
			bands:  append([]uint64(nil), snapEntry.Bands...),
			seq:    snapEntry.Seq,
		}
		index[entry.id] = entry
		buckets[entry.bucket] = append(buckets[entry.bucket], entry)
	}
	if haveSeq && payload.NextSeq <= maxSeq {
		return ErrBadSnapshot
	}

	d.cfg = payload.Config
	d.samples = samples
	d.index = index
	d.buckets = buckets
	d.nextSeq = payload.NextSeq
	return nil
}

func decodeSnapshot(data []byte) (snapshotData, error) {
	var envelope snapshotEnvelope
	if len(data) == 0 || json.Unmarshal(data, &envelope) != nil || len(envelope.Payload) == 0 {
		return snapshotData{}, ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum == "" || envelope.Checksum != hex.EncodeToString(sum[:]) {
		return snapshotData{}, ErrBadSnapshot
	}

	var payload snapshotData
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return snapshotData{}, ErrBadSnapshot
	}
	if payload.Magic != snapshotMagic || payload.Version != snapshotVersion {
		return snapshotData{}, ErrBadSnapshot
	}
	cfg := payload.Config
	if cfg.BandSize <= 0 || cfg.Threshold <= 0 || cfg.MaxPerBucket <= 0 || cfg.TTL <= 0 {
		return snapshotData{}, ErrBadSnapshot
	}
	return payload, nil
}

func equalBands(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Reset drops every sample and every fingerprint.
func (d *CorpusDeduper) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.samples = make(map[string]Sample)
	d.index = make(map[string]*indexEntry)
	d.buckets = make(map[string][]*indexEntry)
	d.nextSeq = 0
}
