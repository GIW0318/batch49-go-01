package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	task "gsb/batch49-go-safety-a-07"
)

var baseTime = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

func evidenceOptions(now func() time.Time) task.Options {
	return task.Options{
		Weights: map[string]int{
			task.SignalEDR:        40,
			task.SignalDiskCrypto: 25,
			task.SignalPatch:      20,
			task.SignalJailbreak:  15,
		},
		Freshness: map[string]time.Duration{
			task.SignalEDR:        10 * time.Minute,
			task.SignalDiskCrypto: 24 * time.Hour,
			task.SignalPatch:      72 * time.Hour,
			task.SignalJailbreak:  24 * time.Hour,
		},
		MaxSignalsPerDevice: 4,
		MaxDevicesPerUser:   1000000,
		Now:                 now,
	}
}

func dumpCommand() int {
	now := baseTime
	a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return now }))

	reportErr := a.Report("dev-1", task.Signal{
		UserID: "user-1", Kind: task.SignalEDR, State: task.EDRInstalled,
		Score: 100, At: baseTime, Seq: 1,
	})
	fmt.Printf("Report:%v\n", reportErr)

	posture, postureErr := a.Posture("dev-1")
	fmt.Printf("Posture:%#v|%v\n", posture, postureErr)

	postures, evaluateErr := a.Evaluate("user-1")
	fmt.Printf("Evaluate:nil=%v len=%d|%v\n", postures == nil, len(postures), evaluateErr)

	fmt.Printf("Quarantine:%v\n", a.Quarantine("dev-1", "manual hold"))
	fmt.Printf("Release:%v\n", a.Release("dev-1", "sec-lead"))
	fmt.Printf("Transfer:%v\n", a.Transfer("dev-1", "user-2"))

	signals := a.Signals("dev-1")
	fmt.Printf("Signals:nil=%v len=%d\n", signals == nil, len(signals))
	audit := a.Audit("user-1")
	fmt.Printf("Audit:nil=%v len=%d\n", audit == nil, len(audit))

	snapshot, snapshotErr := a.Snapshot()
	fmt.Printf("Snapshot:len=%d|%v\n", len(snapshot), snapshotErr)
	fmt.Printf("Restore:%v\n", a.Restore([]byte("<<<not-a-snapshot>>>")))
	return 0
}

type negativeRunner struct {
	checks   int
	failed   int
	failures []string
}

func (r *negativeRunner) expect(name string, ok bool, detail string) {
	r.checks++
	if ok {
		fmt.Printf("PASS %s\n", name)
		return
	}
	r.failed++
	r.failures = append(r.failures, name+": "+detail)
	fmt.Printf("FAIL %s %s\n", name, detail)
}

func (r *negativeRunner) expectErr(name string, err, want error) {
	r.checks++
	if errors.Is(err, want) {
		fmt.Printf("PASS %s\n", name)
		return
	}
	r.failed++
	detail := fmt.Sprintf("got %v want %v", err, want)
	r.failures = append(r.failures, name+": "+detail)
	fmt.Printf("FAIL %s %s\n", name, detail)
}

func (r *negativeRunner) finish() int {
	fmt.Printf("NEGATIVE_RESULT checks=%d failed=%d\n", r.checks, r.failed)
	if r.failed != 0 {
		fmt.Fprintf(os.Stderr, "failures:\n - %s\n", strings.Join(r.failures, "\n - "))
		return 1
	}
	return 0
}

func signal(user, kind string, score int, at time.Time, seq int64) task.Signal {
	return task.Signal{UserID: user, Kind: kind, Score: score, At: at, Seq: seq}
}

func negativeCommand() int {
	r := &negativeRunner{}

	{
		a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
		if err := a.Report("dev-eq-seq", signal("user-1", task.SignalPatch, 100, baseTime, 7)); err != nil {
			r.expect("equal-seq setup", false, err.Error())
		}
		err := a.Report("dev-eq-seq", signal("user-1", task.SignalPatch, 90, baseTime, 7))
		r.expectErr("equal-seq-is-stale", err, task.ErrStaleSignal)
		stored := a.Signals("dev-eq-seq")
		r.expect("equal-seq-does-not-replace", len(stored) == 1 && stored[0].Score == 100 && stored[0].Seq == 7,
			fmt.Sprintf("stored=%+v", stored))
	}

	{
		a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
		r.expectErr("score-negative-rejected",
			a.Report("dev-score", signal("user-1", task.SignalEDR, -1, baseTime, 1)), task.ErrBadScore)
		r.expectErr("score-over-100-rejected",
			a.Report("dev-score", signal("user-1", task.SignalEDR, 101, baseTime, 2)), task.ErrBadScore)
		r.expectErr("score-zero-accepted",
			a.Report("dev-score", signal("user-1", task.SignalEDR, 0, baseTime, 3)), nil)
		posture, err := a.Posture("dev-score")
		r.expect("score-zero-is-quarantined", err == nil && posture.Score == 0 &&
			posture.Tier == task.TierQuarantined && posture.Quarantined,
			fmt.Sprintf("posture=%+v err=%v", posture, err))
	}

	{
		now := baseTime
		a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return now }))
		if err := a.Report("dev-ttl", signal("user-1", task.SignalPatch, 100, baseTime, 1)); err != nil {
			r.expect("ttl setup", false, err.Error())
		}
		now = baseTime.Add(72 * time.Hour)
		posture, err := a.Posture("dev-ttl")
		r.expect("freshness-window-equality-is-fresh", err == nil && posture.Score == 20 && len(posture.Stale) == 0,
			fmt.Sprintf("posture=%+v err=%v", posture, err))
		now = baseTime.Add(72*time.Hour + time.Nanosecond)
		posture, err = a.Posture("dev-ttl")
		r.expect("freshness-after-window-is-stale", err == nil && posture.Score == 8 &&
			len(posture.Stale) == 1 && posture.Stale[0] == task.SignalPatch,
			fmt.Sprintf("posture=%+v err=%v", posture, err))
	}

	{
		a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
		must := []error{
			a.Report("dev-stale", signal("user-1", task.SignalPatch, 100, baseTime.Add(-73*time.Hour), 1)),
			a.Report("dev-stale", signal("user-1", task.SignalEDR, 100, baseTime, 2)),
			a.Report("dev-stale", signal("user-1", task.SignalDiskCrypto, 100, baseTime, 3)),
		}
		for _, err := range must {
			if err != nil {
				r.expect("stale-not-quarantine setup", false, err.Error())
			}
		}
		posture, err := a.Posture("dev-stale")
		r.expect("expiry-alone-does-not-quarantine", err == nil && !posture.Quarantined &&
			posture.Score == 73 && posture.Tier == task.TierDegraded && len(posture.Stale) == 1,
			fmt.Sprintf("posture=%+v err=%v", posture, err))
	}

	{
		a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
		signalsToReport := []task.Signal{
			signal("user-1", task.SignalEDR, 100, baseTime, 1),
			signal("user-1", task.SignalDiskCrypto, 100, baseTime, 2),
			signal("user-1", task.SignalPatch, 100, baseTime, 3),
			signal("user-1", task.SignalJailbreak, 1, baseTime, 4),
		}
		for _, item := range signalsToReport {
			if err := a.Report("dev-hard-jb", item); err != nil {
				r.expect("jailbreak-hard setup", false, err.Error())
			}
		}
		posture, _ := a.Posture("dev-hard-jb")
		r.expect("positive-jailbreak-overrides-high-scores", posture.Quarantined &&
			posture.Tier == task.TierQuarantined && posture.Score == 85, fmt.Sprintf("posture=%+v", posture))
		if err := a.Report("dev-hard-jb", signal("user-1", task.SignalJailbreak, 0, baseTime, 5)); err != nil {
			r.expect("jailbreak-clear setup", false, err.Error())
		}
		posture, _ = a.Posture("dev-hard-jb")
		r.expect("zero-jailbreak-clears-hard-state", !posture.Quarantined && posture.Tier == task.TierCompliant,
			fmt.Sprintf("posture=%+v", posture))
	}

	{
		a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
		removed := signal("user-1", task.SignalEDR, 100, baseTime, 1)
		removed.State = task.EDRRemoved
		if err := a.Report("dev-edr", removed); err != nil {
			r.expect("edr-removed setup", false, err.Error())
		}
		posture, _ := a.Posture("dev-edr")
		r.expect("edr-removed-is-hard-quarantine", posture.Quarantined && posture.Tier == task.TierQuarantined,
			fmt.Sprintf("posture=%+v", posture))
		installed := signal("user-1", task.SignalEDR, 100, baseTime, 2)
		installed.State = task.EDRInstalled
		if err := a.Report("dev-edr", installed); err != nil {
			r.expect("edr-installed setup", false, err.Error())
		}
		posture, _ = a.Posture("dev-edr")
		r.expect("new-edr-heartbeat-clears-hard-state", !posture.Quarantined && posture.Tier == task.TierDegraded,
			fmt.Sprintf("posture=%+v", posture))
	}

	{
		a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
		if err := a.Report("dev-transfer", signal("user-1", task.SignalEDR, 100, baseTime, 1)); err != nil {
			r.expect("transfer setup", false, err.Error())
		}
		if err := a.Quarantine("dev-transfer", "hold"); err != nil {
			r.expect("transfer quarantine setup", false, err.Error())
		}
		if err := a.Transfer("dev-transfer", "user-2"); err != nil {
			r.expect("transfer setup", false, err.Error())
		}
		oldDevices, _ := a.Evaluate("user-1")
		newDevices, _ := a.Evaluate("user-2")
		r.expect("transfer-moves-evaluation", len(oldDevices) == 0 && len(newDevices) == 1 &&
			newDevices[0].UserID == "user-2", fmt.Sprintf("old=%+v new=%+v", oldDevices, newDevices))
		oldAudit := a.Audit("user-1")
		newAudit := a.Audit("user-2")
		r.expect("audit-history-stays-with-old-owner", len(oldAudit) == 3 &&
			oldAudit[2].Action == task.AuditTransferred && len(newAudit) == 0,
			fmt.Sprintf("old=%+v new=%+v", oldAudit, newAudit))
		oldAudit[0].Action = "tampered"
		oldAudit = append(oldAudit, task.AuditEvent{Action: "appended"})
		r.expect("audit-returns-copy", a.Audit("user-1")[0].Action == task.AuditReported,
			fmt.Sprintf("audit=%+v", a.Audit("user-1")))
		r.expectErr("same-owner-transfer", a.Transfer("dev-transfer", "user-2"), task.ErrSameOwner)
		r.expectErr("blank-target-transfer", a.Transfer("dev-transfer", "  "), task.ErrBlankID)
		r.expectErr("unknown-transfer", a.Transfer("dev-missing", "user-2"), task.ErrDeviceNotFound)
	}

	{
		opts := evidenceOptions(func() time.Time { return baseTime })
		a := task.NewDevicePostureAggregator(opts)
		if err := a.Report("dev-snap", signal("user-1", task.SignalPatch, 50, baseTime.Add(-73*time.Hour), 1)); err != nil {
			r.expect("snapshot setup patch", false, err.Error())
		}
		if err := a.Report("dev-snap", signal("user-1", task.SignalEDR, 100, baseTime, 2)); err != nil {
			r.expect("snapshot setup edr", false, err.Error())
		}
		if err := a.Report("dev-snap", signal("user-1", task.SignalDiskCrypto, 100, baseTime, 3)); err != nil {
			r.expect("snapshot setup disk", false, err.Error())
		}
		data, err := a.Snapshot()
		r.expect("snapshot-created", err == nil && len(data) > 0, fmt.Sprintf("err=%v", err))
		restored := task.NewDevicePostureAggregator(opts)
		r.expectErr("random-corrupt-snapshot", restored.Restore([]byte("<<<not-a-snapshot>>>")), task.ErrBadSnapshot)
		if err := restored.Restore(data); err != nil {
			r.expect("valid-snapshot-restore", false, err.Error())
		}
		posture, err := restored.Posture("dev-snap")
		r.expect("restored-original-signal-time-drives-freshness", err == nil && posture.Score == 69 &&
			len(posture.Stale) == 1 && posture.Stale[0] == task.SignalPatch,
			fmt.Sprintf("posture=%+v err=%v", posture, err))
	}

	{
		a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
		if err := a.Report("dev-corrupt", signal("user-1", task.SignalEDR, 100, baseTime, 1)); err != nil {
			r.expect("corrupt setup", false, err.Error())
		}
		r.expectErr("non-json-snapshot-rejected", a.Restore([]byte("<<<bad>>>")), task.ErrBadSnapshot)
		invalidJSON := []byte(`{"version":1,"counter":1,"devices":[{"id":"dev-2","owner":"user-1","signals":[{"user_id":"user-1","kind":"edr_agent","score":101,"seq":1}],"seq":1}],"audit":{}}`)
		r.expectErr("semantically-invalid-snapshot-rejected", a.Restore(invalidJSON), task.ErrBadSnapshot)
		stored := a.Signals("dev-corrupt")
		_, postureErr := a.Posture("dev-corrupt")
		r.expect("failed-restore-leaves-live-state-intact", len(stored) == 1 && stored[0].Score == 100 && postureErr == nil,
			fmt.Sprintf("signals=%+v postureErr=%v", stored, postureErr))
	}

	{
		opts := evidenceOptions(func() time.Time { return baseTime })
		opts.MaxSignalsPerDevice = 1
		opts.MaxDevicesPerUser = 1
		a := task.NewDevicePostureAggregator(opts)
		r.expectErr("first-device-first-kind-allowed",
			a.Report("dev-cap", signal("user-1", task.SignalEDR, 100, baseTime, 1)), nil)
		r.expectErr("signal-cap-enforced",
			a.Report("dev-cap", signal("user-1", task.SignalDiskCrypto, 100, baseTime, 2)), task.ErrSignalLimit)
		r.expectErr("device-cap-enforced",
			a.Report("dev-other", signal("user-1", task.SignalEDR, 100, baseTime, 3)), task.ErrDeviceLimit)
	}

	{
		a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
		r.expectErr("owner-conflict",
			a.Report("dev-owner", signal("user-1", task.SignalEDR, 100, baseTime, 1)), nil)
		r.expectErr("other-owner-rejected",
			a.Report("dev-owner", signal("user-2", task.SignalDiskCrypto, 100, baseTime, 2)), task.ErrDeviceOwner)
		r.expect("owner-conflict-keeps-one-signal", len(a.Signals("dev-owner")) == 1,
			fmt.Sprintf("signals=%+v", a.Signals("dev-owner")))
	}

	{
		a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
		var wg sync.WaitGroup
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_ = a.Report("dev-race", signal("user-1", task.SignalEDR, 100,
					baseTime.Add(time.Duration(i)*time.Minute), int64(i+1)))
			}(i)
		}
		wg.Wait()
		stored := a.Signals("dev-race")
		wantAt := baseTime.Add(63 * time.Minute)
		r.expect("concurrent-reports-keep-time-newest", len(stored) == 1 &&
			stored[0].At.Equal(wantAt) && stored[0].Seq == 64, fmt.Sprintf("stored=%+v", stored))
	}

	return r.finish()
}

type loadResult struct {
	Mode            string  `json:"mode"`
	Functional      bool    `json:"functional"`
	Devices         int     `json:"devices"`
	Users           int     `json:"users"`
	Reports         int     `json:"reports"`
	WallNS          int64   `json:"wall_ns"`
	ReportTotalNS   int64   `json:"report_total_ns"`
	PostureTotalNS  int64   `json:"posture_total_ns"`
	EvaluateTotalNS int64   `json:"evaluate_total_ns"`
	SnapshotNS      int64   `json:"snapshot_ns"`
	RestoreNS       int64   `json:"restore_ns"`
	AvgReportNS     float64 `json:"avg_report_ns"`
	AvgPostureNS    float64 `json:"avg_posture_ns"`
	CoreDeviceNS    float64 `json:"core_device_ns"`
	SnapshotBytes   int     `json:"snapshot_bytes"`
	CoreHeapBytes   uint64  `json:"core_heap_alloc_bytes"`
	CorePeakRSS     uint64  `json:"core_peak_rss_bytes"`
	HeapAllocBytes  uint64  `json:"heap_alloc_bytes"`
	TotalAllocBytes uint64  `json:"total_alloc_bytes"`
	SysBytes        uint64  `json:"sys_bytes"`
	PeakRSSBytes    uint64  `json:"peak_rss_bytes"`
	GCCompletions   uint32  `json:"gc_completions"`
	LastError       string  `json:"last_error,omitempty"`
}

func stressCommand(args []string) int {
	fs := flag.NewFlagSet("stress", flag.ContinueOnError)
	reportCount := fs.Int("reports", 800, "accepted-or-attempted reports")
	snapshotCount := fs.Int("snapshots", 80, "concurrent snapshots")
	writers := fs.Int("writers", 8, "concurrent writers")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := evidenceOptions(func() time.Time { return baseTime })
	a := task.NewDevicePostureAggregator(opts)
	kinds := []string{task.SignalEDR, task.SignalDiskCrypto, task.SignalPatch, task.SignalJailbreak}

	var sequence int64
	var unexpectedErr error
	var errOnce sync.Once
	var writerWG sync.WaitGroup
	for writer := 0; writer < *writers; writer++ {
		writerWG.Add(1)
		go func() {
			defer writerWG.Done()
			for {
				next := atomic.AddInt64(&sequence, 1)
				if next > int64(*reportCount) {
					return
				}
				item := signal("stress-user", kinds[(next-1)%int64(len(kinds))],
					int(next%101), baseTime.Add(time.Duration(next)*time.Millisecond), next)
				if err := a.Report("stress-dev", item); err != nil && !errors.Is(err, task.ErrStaleSignal) {
					errOnce.Do(func() { unexpectedErr = err })
					return
				}
			}
		}()
	}

	var snapshotWG sync.WaitGroup
	for i := 0; i < *snapshotCount; i++ {
		snapshotWG.Add(1)
		go func() {
			defer snapshotWG.Done()
			data, err := a.Snapshot()
			if err != nil {
				errOnce.Do(func() { unexpectedErr = err })
				return
			}
			restored := task.NewDevicePostureAggregator(opts)
			if err := restored.Restore(data); err != nil {
				errOnce.Do(func() { unexpectedErr = err })
			}
		}()
	}

	writerWG.Wait()
	snapshotWG.Wait()

	if unexpectedErr != nil {
		fmt.Printf("STRESS FAIL unexpected_error=%v\n", unexpectedErr)
		return 1
	}

	stored := a.Signals("stress-dev")
	if len(stored) != len(kinds) {
		fmt.Printf("STRESS FAIL signal_count=%d want %d stored=%+v\n", len(stored), len(kinds), stored)
		return 1
	}
	events := a.Audit("stress-user")
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			fmt.Printf("STRESS FAIL audit sequence not increasing at %d: %d after %d\n",
				i, events[i].Seq, events[i-1].Seq)
			return 1
		}
	}

	fmt.Printf("STRESS PASS reports=%d snapshots=%d writers=%d signals=%d audit_events=%d\n",
		*reportCount, *snapshotCount, *writers, len(stored), len(events))
	return 0
}

func loadCommand(args []string) int {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	devices := fs.Int("devices", 20000, "number of devices")
	users := fs.Int("users", 100, "number of users")
	baseline := fs.Bool("baseline", false, "ignore not-implemented baseline errors")
	includeSnapshot := fs.Bool("snapshot", true, "include snapshot and restore phase")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *devices <= 0 || *users <= 0 {
		fmt.Fprintln(os.Stderr, "devices and users must be positive")
		return 2
	}

	opts := evidenceOptions(func() time.Time { return baseTime })
	opts.MaxDevicesPerUser = *devices + 1
	a := task.NewDevicePostureAggregator(opts)
	kinds := []string{task.SignalEDR, task.SignalDiskCrypto, task.SignalPatch, task.SignalJailbreak}

	start := time.Now()
	reportStart := time.Now()
	var sink error
	for i := 0; i < *devices; i++ {
		deviceID := fmt.Sprintf("dev-%06d", i)
		userID := fmt.Sprintf("user-%04d", i%*users)
		for j, kind := range kinds {
			err := a.Report(deviceID, signal(userID, kind, 100, baseTime, int64(j+1)))
			sink = err
			if !*baseline && err != nil {
				fmt.Fprintf(os.Stderr, "report %s %s: %v\n", deviceID, kind, err)
				return 1
			}
		}
	}
	reportNS := time.Since(reportStart).Nanoseconds()

	postureStart := time.Now()
	for i := 0; i < *devices; i++ {
		deviceID := fmt.Sprintf("dev-%06d", i)
		p, err := a.Posture(deviceID)
		sink = err
		if !*baseline && (err != nil || p.Score != 100 || !p.Quarantined || p.Tier != task.TierQuarantined) {
			fmt.Fprintf(os.Stderr, "posture %s: %+v %v\n", deviceID, p, err)
			return 1
		}
	}
	postureNS := time.Since(postureStart).Nanoseconds()

	evaluateStart := time.Now()
	for i := 0; i < *users; i++ {
		userID := fmt.Sprintf("user-%04d", i)
		postures, err := a.Evaluate(userID)
		sink = err
		if !*baseline && err != nil {
			fmt.Fprintf(os.Stderr, "evaluate %s: %v\n", userID, err)
			return 1
		}
		if !*baseline && len(postures) == 0 {
			fmt.Fprintf(os.Stderr, "evaluate %s returned no devices\n", userID)
			return 1
		}
	}
	evaluateNS := time.Since(evaluateStart).Nanoseconds()

	var coreStats runtime.MemStats
	runtime.ReadMemStats(&coreStats)
	corePeakRSS := peakRSSBytes()

	snapshotStart := time.Now()
	var data []byte
	var err error
	var snapshotNS int64
	if *includeSnapshot {
		data, err = a.Snapshot()
		snapshotNS = time.Since(snapshotStart).Nanoseconds()
		sink = err
		if !*baseline && err != nil {
			fmt.Fprintf(os.Stderr, "snapshot: %v\n", err)
			return 1
		}
	}

	restoreStart := time.Now()
	restored := task.NewDevicePostureAggregator(opts)
	var restoreNS int64
	if *includeSnapshot {
		err = restored.Restore(data)
		restoreNS = time.Since(restoreStart).Nanoseconds()
		sink = err
		if !*baseline && err != nil {
			fmt.Fprintf(os.Stderr, "restore: %v\n", err)
			return 1
		}
	}

	wallNS := time.Since(start).Nanoseconds()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	lastError := ""
	if sink != nil {
		lastError = sink.Error()
	}
	result := loadResult{
		Mode:            "load",
		Functional:      !*baseline,
		Devices:         *devices,
		Users:           *users,
		Reports:         *devices * len(kinds),
		WallNS:          wallNS,
		ReportTotalNS:   reportNS,
		PostureTotalNS:  postureNS,
		EvaluateTotalNS: evaluateNS,
		SnapshotNS:      snapshotNS,
		RestoreNS:       restoreNS,
		AvgReportNS:     float64(reportNS) / float64(*devices*len(kinds)),
		AvgPostureNS:    float64(postureNS) / float64(*devices),
		CoreDeviceNS:    float64(reportNS)/float64(*devices) + float64(postureNS)/float64(*devices),
		SnapshotBytes:   len(data),
		CoreHeapBytes:   coreStats.HeapAlloc,
		CorePeakRSS:     corePeakRSS,
		HeapAllocBytes:  stats.HeapAlloc,
		TotalAllocBytes: stats.TotalAlloc,
		SysBytes:        stats.Sys,
		PeakRSSBytes:    peakRSSBytes(),
		GCCompletions:   stats.NumGC,
		LastError:       lastError,
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return 1
	}
	fmt.Println(string(encoded))
	return 0
}

func peakRSSBytes() uint64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

func fixtureSnapshotCommand(path string) int {
	if path == "" {
		fmt.Fprintln(os.Stderr, "snapshot path is required")
		return 2
	}
	a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
	reports := []task.Signal{
		signal("restart-user", task.SignalPatch, 50, baseTime.Add(-73*time.Hour), 1),
		signal("restart-user", task.SignalEDR, 100, baseTime, 2),
		signal("restart-user", task.SignalDiskCrypto, 100, baseTime, 3),
	}
	for _, item := range reports {
		if err := a.Report("dev-restart", item); err != nil {
			fmt.Fprintf(os.Stderr, "report: %v\n", err)
			return 1
		}
	}
	data, err := a.Snapshot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "snapshot: %v\n", err)
		return 1
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "write snapshot: %v\n", err)
		return 1
	}
	fmt.Printf("FIXTURE_SNAPSHOT path=%s bytes=%d sha256=", path, len(data))
	sum := sha256.Sum256(data)
	fmt.Printf("%x\n", sum)
	return 0
}

func restoredPostureCommand(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read snapshot: %v\n", err)
		return 1
	}
	a := task.NewDevicePostureAggregator(evidenceOptions(func() time.Time { return baseTime }))
	if err := a.Restore(data); err != nil {
		fmt.Fprintf(os.Stderr, "restore: %v\n", err)
		return 1
	}
	posture, err := a.Posture("dev-restart")
	if err != nil {
		fmt.Fprintf(os.Stderr, "posture: %v\n", err)
		return 1
	}
	fmt.Printf("RESTORED_POSTURE score=%d tier=%s quarantined=%v stale=%v\n",
		posture.Score, posture.Tier, posture.Quarantined, posture.Stale)
	if posture.Score != 69 || len(posture.Stale) != 1 || posture.Stale[0] != task.SignalPatch {
		fmt.Println("RESTART CHECK FAIL")
		return 1
	}
	fmt.Println("RESTART CHECK PASS")
	return 0
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: posture-evidence <dump|negative|load> [flags]")
		os.Exit(2)
	}

	var code int
	switch os.Args[1] {
	case "dump":
		code = dumpCommand()
	case "negative":
		code = negativeCommand()
	case "stress":
		code = stressCommand(os.Args[2:])
	case "fixture-snapshot":
		if len(os.Args) < 3 {
			code = 2
		} else {
			code = fixtureSnapshotCommand(os.Args[2])
		}
	case "restored-posture":
		if len(os.Args) < 3 {
			code = 2
		} else {
			code = restoredPostureCommand(os.Args[2])
		}
	case "load":
		code = loadCommand(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		code = 2
	}
	os.Exit(code)
}
