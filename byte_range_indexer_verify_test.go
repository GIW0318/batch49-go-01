package task

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func vPair(ticks uint32, sap bool, payload int) []byte {
	head := make([]byte, 8)
	binary.BigEndian.PutUint32(head[0:4], ticks)
	if sap {
		head[4] = 1
	}
	moof := make([]byte, 16)
	binary.BigEndian.PutUint32(moof[0:4], 16)
	copy(moof[4:8], "moof")
	copy(moof[8:], head)
	mdat := make([]byte, 8+payload)
	binary.BigEndian.PutUint32(mdat[0:4], uint32(8+payload))
	copy(mdat[4:8], "mdat")
	return append(moof, mdat...)
}

func TestVerifyConcurrentIndexLocateInvalidate(t *testing.T) {
	b := NewByteRangeIndexer(IndexConfig{Timescale: 1000, MaxEntries: 4096})
	const segs = 32
	var wg sync.WaitGroup
	for i := 0; i < segs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seg := fmt.Sprintf("seg-%02d", i)
			data := append(vPair(10, true, 8), vPair(10, false, 8)...)
			data = append(data, vPair(10, false, 8)...)
			if err := b.IndexSegment(seg, data); err != nil {
				t.Errorf("index %s: %v", seg, err)
			}
		}(i)
	}
	for j := 0; j < 8; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 500; k++ {
				_, _ = b.Locate(time.Duration(k)*time.Millisecond, false)
				_, _ = b.Locate(time.Duration(k)*time.Millisecond, true)
				_ = b.Entries()
				_ = b.TotalBytes()
			}
		}()
	}
	wg.Wait()
	if got := b.Entries(); got != segs*3 {
		t.Fatalf("entries = %d, want %d", got, segs*3)
	}
	var prevOff int64
	var prevEnd time.Duration
	first := true
	for _, id := range b.Segments() {
		for i := 0; i < 3; i++ {
			r, err := b.RangeAt(id, i)
			if err != nil {
				t.Fatalf("range %s/%d: %v", id, i, err)
			}
			if !first {
				if r.Offset != prevOff {
					t.Fatalf("offset gap at %s/%d: got %d, want %d", id, i, r.Offset, prevOff)
				}
				if r.Start != prevEnd {
					t.Fatalf("time gap at %s/%d: got %v, want %v", id, i, r.Start, prevEnd)
				}
			}
			first = false
			prevOff = r.Offset + r.Size
			prevEnd = r.End
		}
	}
	if got := b.TotalBytes(); got != prevOff {
		t.Fatalf("total bytes = %d, want %d", got, prevOff)
	}
	victim := b.Segments()[3]
	third, err := b.RangeAt(victim, 0)
	if err != nil {
		t.Fatalf("victim range: %v", err)
	}
	if err := b.Invalidate(victim); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, err := b.Locate(third.Start+time.Millisecond, false); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("locate into invalidated segment = %v, want ErrOutOfRange", err)
	}
	if got := b.Entries(); got != segs*3-3 {
		t.Fatalf("entries after invalidate = %d, want %d", got, segs*3-3)
	}
	if err := b.Invalidate(victim); !errors.Is(err, ErrUnknownSegment) {
		t.Fatalf("re-invalidate = %v, want ErrUnknownSegment", err)
	}
}

func TestVerifySnapshotRestoreConsistentLocate(t *testing.T) {
	cfg := IndexConfig{Timescale: 1000, MaxEntries: 4096}
	b := NewByteRangeIndexer(cfg)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seg := fmt.Sprintf("seg-%02d", i)
			data := append(vPair(7, i%2 == 0, 5), vPair(13, false, 9)...)
			if err := b.IndexSegment(seg, data); err != nil {
				t.Errorf("index %s: %v", seg, err)
			}
		}(i)
	}
	wg.Wait()
	if err := b.Invalidate("seg-05"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	snap, err := b.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := NewByteRangeIndexer(cfg)
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for ms := 0; ms < 400; ms++ {
		for _, sap := range []bool{false, true} {
			want, errWant := b.Locate(time.Duration(ms)*time.Millisecond, sap)
			got, errGot := restored.Locate(time.Duration(ms)*time.Millisecond, sap)
			if errWant != errGot || want != got {
				t.Fatalf("locate %dms sap=%v diverges: (%v,%v) vs (%v,%v)",
					ms, sap, want, errWant, got, errGot)
			}
		}
	}
	if got, want := restored.Stats(), b.Stats(); got != want {
		t.Fatalf("stats diverge: %+v vs %+v", got, want)
	}
	if err := restored.Restore(snap[:len(snap)/2]); !errors.Is(err, ErrBadSnapshot) {
		t.Fatalf("truncated snapshot = %v, want ErrBadSnapshot", err)
	}
	if got := restored.Entries(); got != 30 {
		t.Fatalf("entries after rejected restore = %d, want 30", got)
	}
}

func TestVerifyTrailingBytesMergeIntoPrevious(t *testing.T) {
	b := NewByteRangeIndexer(IndexConfig{Timescale: 1000, MaxEntries: 16})
	data := append(vPair(40, true, 20), vPair(40, false, 20)...)
	data = append(data, 0xAA, 0xBB, 0xCC)
	if err := b.IndexSegment("seg-t", data); err != nil {
		t.Fatalf("index: %v", err)
	}
	if got := b.Entries(); got != 2 {
		t.Fatalf("entries = %d, want 2", got)
	}
	last, err := b.RangeAt("seg-t", 1)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if last.Size != 47 {
		t.Fatalf("trailing bytes not merged: size = %d, want 47", last.Size)
	}
	if got := b.TotalBytes(); got != int64(len(data)) {
		t.Fatalf("total bytes = %d, want %d", got, len(data))
	}
}

func TestVerifyTickRemainderCarries(t *testing.T) {
	b := NewByteRangeIndexer(IndexConfig{Timescale: 3, MaxEntries: 16})
	data := append(vPair(1, true, 1), vPair(1, false, 1)...)
	data = append(data, vPair(1, false, 1)...)
	if err := b.IndexSegment("seg-r", data); err != nil {
		t.Fatalf("index: %v", err)
	}
	last, err := b.RangeAt("seg-r", 2)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if last.End != time.Second {
		t.Fatalf("accumulated end = %v, want exactly 1s", last.End)
	}
	first, _ := b.RangeAt("seg-r", 0)
	if first.End-first.Start != 333333333*time.Nanosecond {
		t.Fatalf("first duration = %v, want 333333333ns", first.End-first.Start)
	}
}

func TestVerifyTinyTickNeverRoundsToZero(t *testing.T) {
	b := NewByteRangeIndexer(IndexConfig{Timescale: 2000000000, MaxEntries: 16})
	if err := b.IndexSegment("seg-z", vPair(1, true, 1)); err != nil {
		t.Fatalf("index: %v", err)
	}
	r, err := b.RangeAt("seg-z", 0)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if r.End-r.Start != time.Nanosecond {
		t.Fatalf("duration = %v, want 1ns floor", r.End-r.Start)
	}
}

func TestVerifySidxTrailingAndReset(t *testing.T) {
	b := NewByteRangeIndexer(IndexConfig{Timescale: 1000, MaxEntries: 16})
	payload := make([]byte, 8+12)
	binary.BigEndian.PutUint32(payload[0:4], 500)
	binary.BigEndian.PutUint32(payload[4:8], 1)
	binary.BigEndian.PutUint32(payload[8:12], 10)
	binary.BigEndian.PutUint32(payload[12:16], 30)
	binary.BigEndian.PutUint32(payload[16:20], 1)
	sidx := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(sidx[0:4], uint32(len(sidx)))
	copy(sidx[4:8], "sidx")
	copy(sidx[8:], payload)
	data := append(sidx, make([]byte, 30)...)
	if err := b.IndexSegment("seg-s", data); err != nil {
		t.Fatalf("index: %v", err)
	}
	r, err := b.RangeAt("seg-s", 0)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if r.Offset != int64(len(sidx)) || r.Size != 30 || r.End != 20*time.Millisecond || !r.SAP {
		t.Fatalf("sidx range = %+v", r)
	}
	if err := b.IndexSegment("seg-next", vPair(10, true, 1)); err != nil {
		t.Fatalf("index next: %v", err)
	}
	next, err := b.RangeAt("seg-next", 0)
	if err != nil {
		t.Fatalf("next range: %v", err)
	}
	if next.Offset != r.Offset+r.Size {
		t.Fatalf("next offset = %d, want %d (no backtrack)", next.Offset, r.Offset+r.Size)
	}
	if next.Start != 20*time.Millisecond {
		t.Fatalf("next start = %v, want 20ms", next.Start)
	}
	if err := b.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if b.Entries() != 0 || b.TotalBytes() != 0 || len(b.Segments()) != 0 {
		t.Fatalf("reset left state behind: %+v", b.Stats())
	}
}
