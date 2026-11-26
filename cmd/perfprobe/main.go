package main

import (
	"flag"
	"fmt"
	"runtime"
	"time"

	task "gsb/batch49-go-safety-a-12"
)

func main() {
	changes := flag.Int("changes", 100_000, "number of changes to apply")
	batchSize := flag.Int("batch", 8, "changes per Apply call")
	records := flag.Int("records", 128, "region record table capacity")
	audit := flag.Int("audit", 4096, "region audit capacity")
	allowErrors := flag.Bool("allow-errors", false, "ignore Apply errors (baseline stub mode)")
	flag.Parse()

	stream := task.NewIdentityReplicaStream(task.Limits{
		MaxBatch:   *batchSize,
		MaxRecords: *records,
		MaxAudit:   *audit,
		MaxLag:     1 << 30,
	})
	if err := stream.RegisterRegion("region-a"); err != nil && !*allowErrors {
		panic(err)
	}
	identityIDs := make([]string, *records)
	for i := range identityIDs {
		identityIDs[i] = fmt.Sprintf("user-%04d", i)
	}

	stopSampling := make(chan struct{})
	sampleDone := make(chan struct{})
	var maxHeapAlloc uint64
	var maxHeapSys uint64
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		defer close(sampleDone)
		for {
			select {
			case <-ticker.C:
				var stats runtime.MemStats
				runtime.ReadMemStats(&stats)
				if stats.HeapAlloc > maxHeapAlloc {
					maxHeapAlloc = stats.HeapAlloc
				}
				if stats.HeapSys > maxHeapSys {
					maxHeapSys = stats.HeapSys
				}
			case <-stopSampling:
				return
			}
		}
	}()

	applied := 0
	attempted := 0
	errors := 0
	batches := 0
	start := time.Now()
	for base := 1; attempted < *changes; base += *batchSize {
		size := *batchSize
		if remaining := *changes - attempted; remaining < size {
			size = remaining
		}
		batch := make([]task.Change, 0, size)
		for offset := 0; offset < size; offset++ {
			counter := int64(base + offset)
			batch = append(batch, task.Change{
				ID:      identityIDs[counter%int64(*records)],
				Op:      task.OpPut,
				Version: task.Version{Epoch: 1, Counter: counter},
				Origin:  "region-b",
				Name:    "Alice",
			})
		}
		_, err := stream.Apply("region-a", batch)
		batches++
		attempted += size
		if err != nil {
			errors++
			if !*allowErrors {
				panic(err)
			}
			continue
		}
		applied += size
	}
	elapsed := time.Since(start)
	close(stopSampling)
	<-sampleDone

	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	wm, wmErr := stream.Watermark("region-a")
	fmt.Printf("requested_changes=%d\n", *changes)
	fmt.Printf("applied_changes=%d\n", applied)
	fmt.Printf("apply_calls=%d\n", batches)
	fmt.Printf("apply_errors=%d\n", errors)
	fmt.Printf("elapsed_ns=%d\n", elapsed.Nanoseconds())
	fmt.Printf("ns_per_requested_change=%.3f\n", float64(elapsed.Nanoseconds())/float64(*changes))
	fmt.Printf("ns_per_apply_call=%.3f\n", float64(elapsed.Nanoseconds())/float64(batches))
	fmt.Printf("sampled_max_heap_alloc_bytes=%d\n", maxHeapAlloc)
	fmt.Printf("sampled_max_heap_sys_bytes=%d\n", maxHeapSys)
	fmt.Printf("after_gc_heap_alloc_bytes=%d\n", stats.HeapAlloc)
	fmt.Printf("total_alloc_bytes=%d\n", stats.TotalAlloc)
	fmt.Printf("sys_bytes=%d\n", stats.Sys)
	fmt.Printf("gc_count=%d\n", stats.NumGC)
	fmt.Printf("watermark=%d.%d watermark_err=%q\n", wm.Epoch, wm.Counter, errText(wmErr))
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
