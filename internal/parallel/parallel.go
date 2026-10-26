// Package parallel runs per-file work on a bounded set of goroutines.
// Callers write results into per-index slots and merge them in index order,
// so output never depends on scheduling.
package parallel

import (
	"context"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
)

// slots caps concurrent jobs across every Run in the process. Sources load
// concurrently; without a shared cap each would start GOMAXPROCS workers and
// the largest file would get a fraction of a core, becoming the tail.
var slots = make(chan struct{}, runtime.GOMAXPROCS(0))

type limitKey struct{}

// WithLimit returns a context under which Run uses at most n workers
// (1 runs jobs one at a time, as --single-thread asks).
func WithLimit(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, limitKey{}, max(1, n))
}

// Run calls fn(i) for each index in order on up to workers goroutines (fewer
// under WithLimit), handing indexes out in the order given. Each call holds one of the
// process-wide slots, so fn must not itself call Run. Run stops handing out
// indexes once ctx is done and then returns ctx.Err().
func Run(ctx context.Context, workers int, order []int, fn func(i int)) error {
	return run(ctx, workers, order, true, fn)
}

// run is Run, holding a process-wide slot for each call of fn when slotted.
func run(ctx context.Context, workers int, order []int, slotted bool, fn func(i int)) error {
	if limit, ok := ctx.Value(limitKey{}).(int); ok {
		workers = min(workers, limit)
	}
	workers = max(1, min(workers, len(order)))
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				k := int(next.Add(1) - 1)
				if k >= len(order) {
					return
				}
				if slotted {
					select {
					case slots <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
				if ctx.Err() == nil {
					fn(order[k])
				}
				if slotted {
					<-slots
				}
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// LargestFirst returns the indexes of paths ordered by descending file size,
// so the longest jobs start first and do not become the tail of a parallel
// run. Files that cannot be stat'ed keep their relative order at the end.
// The stats hold no slots: they are quick, and waiting for other sources'
// jobs to free one would hold up this run's start.
func LargestFirst(ctx context.Context, workers int, paths []string) []int {
	sizes := make([]int64, len(paths))
	order := make([]int, len(paths))
	for i := range order {
		order[i] = i
	}
	_ = run(ctx, workers, order, false, func(i int) {
		sizes[i] = -1
		if info, err := os.Stat(paths[i]); err == nil {
			sizes[i] = info.Size()
		}
	})
	sort.SliceStable(order, func(a, b int) bool { return sizes[order[a]] > sizes[order[b]] })
	return order
}
