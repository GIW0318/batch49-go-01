package task

import "testing"

var (
	benchApplyResult ApplyResult
	benchApplyError  error
)

func BenchmarkApplyCoreSingle(b *testing.B) {
	s := NewIdentityReplicaStream(Limits{MaxBatch: 8, MaxRecords: 64, MaxAudit: 32, MaxLag: 4})
	if err := s.RegisterRegion("region-a"); err != nil {
		b.Fatal(err)
	}
	changes := []Change{{
		ID:      "u1",
		Op:      OpPut,
		Version: Version{Epoch: 1, Counter: 1},
		Origin:  "region-b",
		Name:    "Alice",
	}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		changes[0].Version.Counter = int64(i + 1)
		result, err := s.Apply("region-a", changes)
		if err != nil || result.Applied != 1 {
			b.Fatalf("apply result=%+v err=%v", result, err)
		}
		benchApplyResult = result
		benchApplyError = err
	}
}

func BenchmarkApplyCoreBatch8(b *testing.B) {
	s := NewIdentityReplicaStream(Limits{MaxBatch: 8, MaxRecords: 64, MaxAudit: 32, MaxLag: 4})
	if err := s.RegisterRegion("region-a"); err != nil {
		b.Fatal(err)
	}
	changes := make([]Change, 8)
	for i := range changes {
		changes[i] = Change{ID: "u1", Op: OpPut, Origin: "region-b", Name: "Alice"}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		base := int64(i*8 + 1)
		for j := range changes {
			changes[j].Version = Version{Epoch: 1, Counter: base + int64(j)}
		}
		result, err := s.Apply("region-a", changes)
		if err != nil || result.Applied != 8 {
			b.Fatalf("apply result=%+v err=%v", result, err)
		}
		benchApplyResult = result
		benchApplyError = err
	}
}

func BenchmarkApplySwitchOffBaseline(b *testing.B) {
	s := NewIdentityReplicaStream(Limits{MaxBatch: 8, MaxRecords: 64, MaxAudit: 32, MaxLag: 4})
	s.legacy = true
	changes := []Change{{
		ID:      "u1",
		Op:      OpPut,
		Version: Version{Epoch: 1, Counter: 1},
		Origin:  "region-b",
		Name:    "Alice",
	}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		changes[0].Version.Counter = int64(i + 1)
		result, err := s.Apply("region-a", changes)
		benchApplyResult = result
		benchApplyError = err
	}
}
