package parallel

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestRunVisitsEveryIndexOnce(t *testing.T) {
	order := []int{3, 1, 4, 0, 2}
	var seen [5]atomic.Int32
	if err := Run(context.Background(), 3, order, func(i int) { seen[i].Add(1) }); err != nil {
		t.Fatal(err)
	}
	for i := range seen {
		if seen[i].Load() != 1 {
			t.Fatalf("index %d visited %d times", i, seen[i].Load())
		}
	}
}

func TestRunStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if err := Run(ctx, 1, []int{0, 1, 2}, func(int) { calls++ }); err == nil || calls != 0 {
		t.Fatalf("err=%v calls=%d, want context error and no calls", err, calls)
	}
}

func TestLargestFirst(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i, size := range []int{10, 30, 0, 20} {
		p := filepath.Join(dir, string(rune('a'+i)))
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	paths = append(paths, filepath.Join(dir, "missing"))
	if got, want := LargestFirst(context.Background(), 2, paths), []int{1, 3, 0, 2, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestWithLimitRunsSequentially(t *testing.T) {
	var running, peak atomic.Int32
	order := make([]int, 50)
	for i := range order {
		order[i] = i
	}
	err := Run(WithLimit(context.Background(), 1), 16, order, func(int) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		running.Add(-1)
	})
	if err != nil || peak.Load() != 1 {
		t.Fatalf("err=%v peak concurrency=%d, want 1", err, peak.Load())
	}
}

func TestRunStopsWaitingForSlotWhenCanceled(t *testing.T) {
	// Hold every slot, as other concurrent runs would.
	for i := 0; i < cap(slots); i++ {
		slots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(slots); i++ {
			<-slots
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	var calls atomic.Int32
	go func() { done <- Run(ctx, 4, []int{0, 1, 2, 3}, func(int) { calls.Add(1) }) }()
	cancel()
	if err := <-done; err == nil || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d, want context error and no calls", err, calls.Load())
	}
}
