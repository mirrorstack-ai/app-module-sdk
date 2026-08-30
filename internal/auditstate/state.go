// Package auditstate links audit.Record to the enclosing ms.Tx without adding
// a public callback, context setter, or SQL-string inspection seam.
package auditstate

import (
	"sync/atomic"

	"github.com/mirrorstack-ai/app-module-sdk/db"
)

// Querier preserves db.Querier while tracking whether an audit insert
// succeeded through this transaction.
type Querier struct {
	db.Querier
	recorded atomic.Bool
}

// Track wraps the querier passed into one ms.Tx callback.
func Track(q db.Querier) *Querier { return &Querier{Querier: q} }

// Mark records a successful audit insert when q belongs to ms.Tx. Direct DB
// callers remain valid; they use the explicit ms.DrainAudit recovery hook.
func Mark(q db.Querier) {
	if tracked, ok := q.(*Querier); ok {
		tracked.recorded.Store(true)
	}
}

// Recorded reports whether audit.Record successfully inserted through q.
func (q *Querier) Recorded() bool { return q.recorded.Load() }
