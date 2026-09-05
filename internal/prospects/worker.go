// Package prospects keeps the prospect-memory layer current.
//
// Webhooks give freshness; this sweep gives correctness. Webhook deliveries
// fail, Smartlead offers no replay, and subscriptions are registered by hand
// (registering one would be a write, which the integration forbids). So a
// periodic pass re-derives everything from stored data: anything a webhook
// missed is delayed, never lost.
package prospects

import (
	"context"
	"log/slog"
	"time"

	"github.com/manishkumar/outreachcrm/internal/store"
)

// Worker runs the periodic maintenance sweep.
type Worker struct {
	Store    *store.Store
	Interval time.Duration

	// WorkspaceID scopes the sweep; zero covers every workspace.
	WorkspaceID int64

	// ClassifyBatch caps how many replies are classified per tick, so a large
	// backlog is worked through over several passes instead of one long stall.
	ClassifyBatch int
}

// Result reports one sweep.
type Result struct {
	Classified int
	Scored     int
	Took       time.Duration
}

// Run sweeps on an interval until the context is cancelled. The first sweep
// happens immediately so a restart does not leave scores stale for an hour.
func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.Store == nil {
		return
	}
	interval := w.Interval
	if interval <= 0 {
		interval = 6 * time.Hour
	}

	if res, err := w.Sweep(); err != nil {
		slog.Warn("prospect sweep", "err", err)
	} else if res.Classified > 0 || res.Scored > 0 {
		slog.Info("prospect sweep", "classified", res.Classified, "scored", res.Scored, "took", res.Took.String())
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			res, err := w.Sweep()
			if err != nil {
				slog.Warn("prospect sweep", "err", err)
				continue
			}
			if res.Classified > 0 || res.Scored > 0 {
				slog.Info("prospect sweep", "classified", res.Classified, "scored", res.Scored, "took", res.Took.String())
			}
		}
	}
}

// Sweep classifies any replies behind the current classifier, then recomputes
// every score.
//
// Identity resolution is deliberately not part of this: auto-merge alters live
// records, and that stays an explicit decision rather than something a timer
// does unattended.
func (w *Worker) Sweep() (Result, error) {
	start := time.Now()
	var res Result

	n, err := w.Store.ClassifyReplies(w.WorkspaceID, w.ClassifyBatch)
	if err != nil {
		return res, err
	}
	res.Classified = n

	scored, err := w.Store.RecomputeSignals(w.WorkspaceID)
	if err != nil {
		return res, err
	}
	res.Scored = scored
	res.Took = time.Since(start)
	return res, nil
}
