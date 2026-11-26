package main

import (
	"fmt"

	task "gsb/batch49-go-safety-a-12"
)

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func printErr(label string, err error) {
	fmt.Printf("%s: err=%q\n", label, errText(err))
}

func main() {
	fmt.Println("probe=identity-replica-stream")
	fmt.Printf("CompareVersion(1.1,1.2)=%d\n", task.CompareVersion(task.Version{Epoch: 1, Counter: 1}, task.Version{Epoch: 1, Counter: 2}))
	fmt.Printf("CompareVersion(2.1,1.9)=%d\n", task.CompareVersion(task.Version{Epoch: 2, Counter: 1}, task.Version{Epoch: 1, Counter: 9}))

	stream := task.NewIdentityReplicaStream(task.Limits{
		MaxBatch:   8,
		MaxRecords: 64,
		MaxAudit:   32,
		MaxLag:     4,
	})

	printErr("RegisterRegion(blank)", stream.RegisterRegion("   "))
	printErr("RegisterRegion(region-a)", stream.RegisterRegion("region-a"))

	batch := []task.Change{{
		ID:      "user-1",
		Op:      task.OpPut,
		Version: task.Version{Epoch: 1, Counter: 1},
		Origin:  "region-b",
		Name:    "Alice",
	}}
	result, err := stream.Apply("region-a", batch)
	fmt.Printf("Apply: result=%+v err=%q\n", result, errText(err))

	printErr("Write", stream.Write("region-a", "user-1", "Alice", task.Version{Epoch: 1, Counter: 2}))
	watermark, err := stream.Watermark("region-a")
	fmt.Printf("Watermark: version=%+v err=%q\n", watermark, errText(err))
	lag, err := stream.Lag("region-a")
	fmt.Printf("Lag: lag=%d err=%q\n", lag, errText(err))
	fmt.Printf("GlobalVersion: %+v\n", stream.GlobalVersion())
	printErr("Tombstone", stream.Tombstone("user-1", task.Version{Epoch: 1, Counter: 3}))
	record, err := stream.Read("region-a", "user-1")
	fmt.Printf("Read: record=%+v err=%q\n", record, errText(err))
	readOnly, err := stream.ReadOnly("region-a")
	fmt.Printf("ReadOnly: readOnly=%t err=%q\n", readOnly, errText(err))
	audit := stream.Audit("region-a")
	fmt.Printf("Audit: nil=%t len=%d\n", audit == nil, len(audit))
	checkpoint, err := stream.Checkpoint("region-a")
	fmt.Printf("Checkpoint: nil=%t len=%d err=%q\n", checkpoint == nil, len(checkpoint), errText(err))
	printErr("Resume", stream.Resume("region-a", []byte("fixed-probe-payload")))
	snapshot, err := stream.Snapshot()
	fmt.Printf("Snapshot: nil=%t len=%d err=%q\n", snapshot == nil, len(snapshot), errText(err))
	printErr("Restore", stream.Restore([]byte("fixed-probe-payload")))
}
