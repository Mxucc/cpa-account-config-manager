package manager

import (
	"context"
	"sync"
	"time"
)

// The model-control tabs decorate their rows with the models that CPA AI-provider channels
// list. That scan is an outbound management call with a 15s client timeout, so it must never
// decide how long an operator waits for a click: reads bound it, and writes answer from the
// cache while the scan is refreshed in the background.

// modelControlChannelRefreshTimeout is a variable so tests can shrink it; production uses the
// five-second bound.
var modelControlChannelRefreshTimeout = 5 * time.Second

// modelControlRefreshMu single-flights the background scan, so a burst of clicks cannot queue
// up CPA calls.
var modelControlRefreshMu sync.Mutex

// boundedModelControlContext caps one synchronous channel scan.
func boundedModelControlContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, modelControlChannelRefreshTimeout)
}

// refreshModelControlChannels runs one channel scan in the background. The scan only writes
// into the package-level cache, which is mutex-guarded. A caller that cannot take the slot
// skips the refresh instead of queueing behind it.
func refreshModelControlChannels(scan func(context.Context)) {
	if scan == nil || !modelControlRefreshMu.TryLock() {
		return
	}
	go func() {
		defer modelControlRefreshMu.Unlock()
		ctx, cancel := boundedModelControlContext(context.Background())
		defer cancel()
		scan(ctx)
	}()
}
