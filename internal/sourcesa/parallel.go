package sourcesa

import (
	"context"
	"runtime"

	"github.com/RedwindA/ccusage_go/internal/parallel"
)

// parallelEach calls fn for every file index on up to GOMAXPROCS goroutines,
// largest files first. Callers write results into per-index slots and merge
// them in index order afterwards, so output never depends on scheduling. It
// stops handing out work once ctx is done and then returns ctx.Err().
func parallelEach(ctx context.Context, files []string, fn func(i int)) error {
	workers := runtime.GOMAXPROCS(0)
	return parallel.Run(ctx, workers, parallel.LargestFirst(ctx, workers, files), fn)
}
