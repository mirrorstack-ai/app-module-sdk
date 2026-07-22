package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The DEV-PLANE ANALOGUE of the platform's module_version_exposed_tables
// catalog. Production resolves a cross-module read from a catalog the platform
// owns and freezes at publish; a co-located `mirrorstack dev` session has no
// platform and therefore no catalog, so each module self-publishes the two
// facts a consumer needs — its Config.ID and its registry.ExposedTables() set —
// into one shared dev-only table at boot. That self-publication is not a new
// trust assumption: system/manifest.go:209 builds the manifest's `exposes` from
// the same reg.ExposedTables() call, and prod freezes THAT self-assertion at
// publish time. The dev directory just carries it over a different channel.
//
// WHY THE DATABASE AND NOT ENV OR HTTP. `mirrorstack dev` calls env_clear() on
// every module it spawns (mirrorstack-cli/src/commands/dev/mod.rs:706) and
// re-adds exactly MS_LOCAL_DB_URL, PORT, MS_MODULE_ID, MS_PLATFORM_TOKEN_FILE
// plus PATH and a small infra passthrough allowlist. MS_MODULE_ID_<SLUG> is
// deliberately excluded — commit 5a37ee4 added the clear plus two regression
// tests specifically to kill that sibling-id leak, so building discovery on it
// would resurrect a fixed bug and break on the next CLI rebuild. A sibling's
// PORT is not in the child env either, so a loopback manifest fetch has nothing
// to aim at. MS_LOCAL_DB_URL is the one guaranteed cross-module channel, and it
// is already the SDK's canonical "co-located dev session" signal.
//
// WHY `public` AND NOT A PER-APP SCHEMA. Exposure is a module-VERSION property,
// not a per-app one. Prod anchors it to the version the producer provably runs
// in that app; dev has exactly one version — the running source — so a
// session-global row in the one schema every module shares is the honest
// analogue. Anchoring it per-app would invent an app dimension the fact does
// not have, and would need every producer to have been touched for that app
// before any consumer could resolve it.
//
// WHY A ROW IS LEASED RATHER THAN WRITTEN ONCE. The directory's whole job is to
// answer ONE question — "is this producer co-located RIGHT NOW?" — and the
// caller turns a yes into a local read that BYPASSES the read-exposed proxy.
// A boot-only row cannot answer that question: it answers "did this producer
// boot at some point since the database was created". The two diverge the
// moment a module is stopped, and the divergence is not benign. The relation
// the local read composes still EXISTS in the app schema (a stopped module's
// tables are not dropped), so the read SUCCEEDS and returns whatever the dead
// module last wrote — most often zero rows, with err == nil. That is a silent
// empty, which this SDK forbids everywhere else, and it is strictly worse than
// the proxy fallthrough it displaced. So the row is a LEASE: refreshed by a
// heartbeat while the process lives, deleted on a clean shutdown, and ignored
// by every reader once it ages out. Expiry degrades to a directory MISS, which
// is the additive proxy fallthrough — the correct answer for a producer that is
// no longer here.
//
// KNOWN LIMITS (this is an honest dev-mode fail-fast, not a security boundary —
// production enforcement is the DB role's grants):
//   - the local pool connects as `mirrorstack`, a single rolsuper=t role, and
//     pg_roles holds ZERO r_<app8>_<mod> roles, so there is no GRANT ceiling
//     under this table: any module can UPDATE another module's row and claim
//     an exposure set that is not its own;
//   - a module can skip the SDK entirely (db.Open()) and read a sibling's
//     tables without consulting the directory at all;
//   - a producer killed with SIGKILL (or a laptop that sleeps) cannot run its
//     shutdown delete, so its row outlives it by up to devDirectoryTTL. Within
//     that window a consumer still resolves it as co-located and reads its last
//     written rows. This is a BOUNDED wrong answer, not an unbounded one, and
//     it is the reason the TTL is short rather than generous. There is no
//     read-time backstop underneath it: the earlier claim that a stale row
//     "still fails closed at read time with 42P01" was verified FALSE against
//     the live dev Postgres — the producer's relation is still present in the
//     app schema after the producer stops, so the read succeeds.
//
// What this buys is the thing that actually matters in dev: an HONEST module
// cannot accidentally read what production would refuse.

// devDirectoryTable is the one dev-only relation this file owns.
const devDirectoryTable = `public.ms_dev_module_directory`

// The lease clock. THREE numbers, and they are related — change one and you
// have to re-argue the other two.
//
//   - devDirectoryHeartbeat is how often a live producer re-stamps updated_at.
//     30s is cheap (one UPSERT per module per 30s against a local Postgres) and
//     far longer than any dev restart, so it never contends with boot.
//   - devDirectoryTTL is how long a reader honors a row after its last stamp.
//     THREE missed heartbeats. One or two would flap a perfectly healthy
//     session onto the proxy over a laptop sleep, a stop-the-world GC pause or
//     a `dlv` breakpoint — and flapping is worse than a bounded stale window,
//     because the proxy returns PROD rows and silently changes what the
//     developer sees mid-session. Three beats tolerates that noise while still
//     bounding a SIGKILLed producer's afterlife to 90 seconds.
//   - devDirectoryCacheTTL is how long a CONSUMER reuses a lookup result
//     in-process (DEFECT 11's memo). It is deliberately tiny relative to the
//     TTL: worst-case observed staleness is TTL + cacheTTL, so 5s adds ~5% to
//     the 90s window rather than doubling it, while still collapsing a
//     request-per-read directory query into at most one per producer per 5s.
//
// All three live here so exactly one place decides whether a producer counts as
// co-located right now.
const (
	devDirectoryHeartbeat = 30 * time.Second
	devDirectoryTTL       = 90 * time.Second
	devDirectoryCacheTTL  = 5 * time.Second

	// devDirectoryBootTimeout bounds the SYNCHRONOUS boot publish, which sits on
	// the path to ListenAndServe and starts by taking an advisory lock. Generous
	// for a local CREATE TABLE plus one upsert; short enough that a wedged peer
	// costs a degraded publish instead of a module that never serves.
	devDirectoryBootTimeout = 5 * time.Second
)

// devDirectoryLockKey serializes the CREATE TABLE in ensureDevDirectory across
// concurrently booting modules. Derived from the table name by FNV-1a rather
// than hand-picked so the value is reproducible from its meaning and cannot
// silently collide with a differently-named future advisory lock in this SDK.
// pg_advisory_xact_lock takes a bigint; FNV-1a/64 is folded into int64 by
// simple reinterpretation, which is fine — the key only has to be STABLE.
var devDirectoryLockKey = func() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("mirrorstack:" + devDirectoryTable))
	return int64(h.Sum64())
}()

// errDevDirectoryNoSlug distinguishes "there was nothing to publish" from "the
// publish failed". A module without a Config.Slug cannot be addressed by any
// consumer's DependsOn reference, so it has no directory identity to claim —
// that is a supported dev configuration, not an error, and it must not be
// reported to the developer as a successful publish.
var errDevDirectoryNoSlug = errors.New("dev directory: module has no Config.Slug")

// devModuleEntry is one co-located producer's self-published identity.
// ModuleID is the producer's Config.ID — already in the platform-minted
// m<32 hex> form, so it does NOT need another "m" prefix anywhere downstream.
// Exposes is its registry.ExposedTables() set.
type devModuleEntry struct {
	ModuleID string
	Slug     string
	Exposes  []string
}

// devDirectoryState is every piece of per-process dev-directory machinery,
// grouped so Module carries one field instead of eight.
//
// The three func fields are the package's ONLY I/O seam onto Postgres for this
// feature. They exist as fields rather than direct method calls so the whole
// matrix — detection, the boot publish ordering, authorization, naming and
// every sentinel — is testable with no database and no network. Defaulted in
// New(); never nil.
type devDirectoryState struct {
	lookup  func(context.Context, string) (devModuleEntry, bool, error)
	ensure  func(context.Context) error
	publish func(context.Context) error

	// published records whether THIS module's own row made it into the
	// directory at boot. False means co-located consumers cannot see this
	// module at all, which is the silent no-op DEFECT 2 was about; republish is
	// the one-shot lazy retry that heals it.
	published atomic.Bool
	republish sync.Once

	// announcedPublish gates the publish log to STATE TRANSITIONS. The
	// heartbeat re-publishes every devDirectoryHeartbeat for the life of the
	// session, so logging each success buries the once-per-producer consent
	// warning under one identical line per module per beat. Cleared on a failed
	// beat, so the recovery a developer staring at a 503 actually needs to see
	// still prints — that recovery, and the boot publish, are the only two
	// events worth a line.
	announcedPublish atomic.Bool

	// cache memoizes producer-ref lookups for devDirectoryCacheTTL.
	// Keyed by the normalized producer ref, valued by *devDirectoryCached.
	cache sync.Map

	// announced tracks which producers this process has already logged a
	// co-located resolution for, so the parity warning fires once per producer
	// rather than once per read.
	announced sync.Map

	// started is set by startDevDirectoryLease and read by
	// stopDevDirectoryLease, which must only wait on done if there is a
	// goroutine that will ever close it. A module that never entered the dev
	// lifecycle (Lambda, task worker, a unit test) has no heartbeat, and
	// blocking its Close() on a channel nobody closes would hang shutdown.
	started  atomic.Bool
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// devDirectoryCached is one memoized lookup outcome. Immutable once stored, so
// concurrent readers need no lock beyond the sync.Map itself. Errors are never
// cached: a transient directory failure must not pin a consumer to the proxy
// for the whole cache window.
type devDirectoryCached struct {
	entry devModuleEntry
	ok    bool
	at    time.Time
}

// newDevDirectoryState binds the real Postgres-backed implementations. Called
// from New() so the seams are never nil in production code.
func newDevDirectoryState(m *Module) *devDirectoryState {
	return &devDirectoryState{
		lookup:  m.readDevDirectory,
		ensure:  m.ensureDevDirectory,
		publish: m.registerInDevDirectory,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// devDirectoryUUIDPattern recognizes the dashed-UUID producer ref form.
// parseProducerRef permits hyphens (it only strips owner and semver), and
// DependencyDB's doc promises slug / m<hex> / dashed-UUID all work, so the
// lookup has to normalize the third form to the m<hex> the platform mints.
var devDirectoryUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ensureDevDirectory creates the directory table if absent.
//
// CREATE TABLE IF NOT EXISTS is idempotent but NOT concurrency-safe: the
// existence check and the catalog insert are not atomic against each other, so
// two modules booting at the same instant — which is exactly what
// `mirrorstack dev --all` does — can both pass the check and then collide in
// pg_class. Postgres surfaces that as 23505 (unique_violation on a shared
// catalog index) or 42P07 (duplicate_table), never as success. Both outcomes
// mean THE RELATION EXISTS, which is the entire postcondition this function
// owes its caller, so both are treated as success.
//
// The advisory lock in front of the DDL is the primary fix and the error
// tolerance is the backstop, deliberately both: the lock only serializes
// processes running THIS code, so it cannot cover a psql session or a future
// non-SDK writer, and the tolerance costs one error-code comparison.
//
// There is deliberately NO unique index on slug. Two modules claiming one slug
// is a real (if pathological) local state, and enforcing uniqueness here would
// turn it into a 23505 that fails a module's BOOT. Ambiguity is instead
// resolved by registerInDevDirectory's slug reclamation and, failing that, at
// lookup time — where it can degrade to "use the proxy" instead of "this module
// does not start".
func (m *Module) ensureDevDirectory(ctx context.Context) error {
	pool, release, err := m.resolvePool(ctx)
	if err != nil {
		return fmt.Errorf("dev directory: open pool: %w", err)
	}
	defer release()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("dev directory: begin: %w", err)
	}
	// Rollback after a successful Commit is a documented no-op in pgx, so the
	// defer is safe on both paths and is the only thing that releases the
	// connection on an early return.
	defer func() { _ = tx.Rollback(ctx) }()

	// pg_advisory_xact_lock (not the session-scoped variant) so the lock is
	// released by COMMIT/ROLLBACK no matter how this function exits — a session
	// lock leaked onto a pooled connection would deadlock the next module to
	// boot on that same connection.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, devDirectoryLockKey); err != nil {
		return fmt.Errorf("dev directory: advisory lock: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS `+devDirectoryTable+` (
			module_id  text PRIMARY KEY,
			slug       text NOT NULL,
			exposes    jsonb NOT NULL DEFAULT '[]'::jsonb,
			updated_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		if isRelationAlreadyExists(err) {
			// The tx is already aborted, so there is nothing to commit; the
			// deferred Rollback cleans up. The postcondition holds regardless.
			return nil
		}
		return fmt.Errorf("dev directory: create table: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("dev directory: commit: %w", err)
	}
	return nil
}

// isRelationAlreadyExists reports whether err is Postgres telling us the
// relation we tried to create is already there. 42P07 is the direct
// duplicate_table verdict; 23505 is the same race observed one layer down, as a
// unique violation on the pg_class/pg_type catalog index, and is what
// CREATE TABLE IF NOT EXISTS actually raises when two backends interleave
// between the existence check and the insert.
func isRelationAlreadyExists(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "42P07" || pgErr.Code == "23505"
}

// registerInDevDirectory upserts THIS module's row and reclaims its slug.
//
// Skipped with one loud log line when Config.Slug is empty: Slug is optional
// (module.go:48) and a module without one simply is not locally discoverable
// as a producer by the slug form every consumer's DependsOn actually uses.
// Publishing a row with an empty slug would be worse than publishing none — it
// would match a consumer that resolved to the empty string.
//
// SLUG RECLAMATION. The DELETE in the same batch drops any OTHER module_id
// currently claiming this slug. Two things make that necessary rather than
// tidy. First, a module's Config.ID legitimately changes across dev sessions
// (a re-registered module mints a new id), and without reclamation the old id
// keeps answering to the slug forever — readDevDirectory would then see two
// rows for one ref and refuse the lookup as ambiguous, silently demoting a
// working session to the proxy. Second, it is the only thing that bounds this
// table's growth: rows are otherwise immortal across every dev session the
// developer ever runs.
//
// Batched rather than run as two Execs so the window in which the slug has two
// claimants (or none) never spans a round trip. pgx sends a batch as one
// implicit transaction.
func (m *Module) registerInDevDirectory(ctx context.Context) error {
	if m.config.Slug == "" {
		m.logger.Printf("dev: module %s has no Config.Slug, so it is not discoverable as a co-located dependency producer (co-located consumers will use the read-exposed proxy for it)", m.config.ID)
		return errDevDirectoryNoSlug
	}

	// ExposedTables already returns a sorted, non-nil slice
	// (internal/registry/exposure.go:46), so the marshalled JSON is stable and
	// an unchanged exposure set rewrites byte-identical content.
	exposed := m.registry.ExposedTables()
	exposes, err := json.Marshal(exposed)
	if err != nil {
		return fmt.Errorf("dev directory: marshal exposed tables: %w", err)
	}

	pool, release, err := m.resolvePool(ctx)
	if err != nil {
		return fmt.Errorf("dev directory: open pool: %w", err)
	}
	defer release()

	batch := &pgx.Batch{}
	batch.Queue(`
		INSERT INTO `+devDirectoryTable+` (module_id, slug, exposes, updated_at)
		VALUES ($1, $2, $3::jsonb, now())
		ON CONFLICT (module_id) DO UPDATE
		   SET slug = EXCLUDED.slug, exposes = EXCLUDED.exposes, updated_at = now()`,
		m.config.ID, m.config.Slug, string(exposes))
	batch.Queue(`
		DELETE FROM `+devDirectoryTable+`
		 WHERE slug = $1 AND module_id <> $2`,
		m.config.Slug, m.config.ID)

	if err := pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("dev directory: upsert %s: %w", m.config.ID, err)
	}
	return nil
}

// startDevDirectoryLease publishes this module's row and then keeps the lease
// alive for the life of the process.
//
// The heartbeat is what makes a directory hit mean "co-located RIGHT NOW"
// rather than "booted at some point" — see the lease rationale in this file's
// header. It re-runs the same upsert registerInDevDirectory does, so there is
// one publish statement, not a publish and a separate touch that could drift on
// the exposure set (which a live `ms.ExposeTable` edit + restart does change).
//
// Failures are logged and retried on the next tick rather than escalated: a
// blip in the local Postgres should cost at most one heartbeat, and the TTL
// gives it three chances before any consumer notices.
func (m *Module) startDevDirectoryLease(ctx context.Context) {
	if err := m.publishDevDirectory(ctx); err != nil {
		m.logger.Printf("dev: could not publish this module to the dependency directory: %v", err)
	}
	m.devDir.started.Store(true)
	go func() {
		defer close(m.devDir.done)
		t := time.NewTicker(devDirectoryHeartbeat)
		defer t.Stop()
		for {
			select {
			case <-m.devDir.stop:
				return
			case <-t.C:
				// Background, not the boot ctx: a boot context that gets
				// cancelled must not silently end the lease for the rest of the
				// process's life.
				hbCtx, cancel := context.WithTimeout(context.Background(), devDirectoryHeartbeat)
				if err := m.publishDevDirectory(hbCtx); err != nil {
					m.logger.Printf("dev: dependency directory heartbeat failed (co-located consumers fall back to the read-exposed proxy after %s): %v", devDirectoryTTL, err)
				}
				cancel()
			}
		}
	}()
}

// publishDevDirectory runs ensure-then-register and records the outcome.
//
// ORDERING CONTRACT, and the reason this is not inlined: a failing `ensure`
// must NOT skip `publish`. The overwhelmingly likely cause of an ensure failure
// under `mirrorstack dev --all` is a racing peer that is creating the very same
// table, in which case the table exists and the publish succeeds. Returning
// early on ensure — as this code originally did — turned that transient race
// into a permanent, silent no-op for the whole process lifetime: the module
// never appears in the directory, every consumer of it falls back to the proxy,
// and the 503 this feature exists to fix comes straight back with nothing in
// the logs to explain it.
func (m *Module) publishDevDirectory(ctx context.Context) error {
	ensureErr := m.devDir.ensure(ctx)
	if ensureErr != nil {
		m.logger.Printf("dev: dependency directory setup failed, attempting to publish anyway (a peer module has very likely already created the table): %v", ensureErr)
	}
	if err := m.devDir.publish(ctx); err != nil {
		// NOTHING TO PUBLISH is not a failed publish. A slugless module is a
		// supported dev configuration (Config.Slug is optional), and
		// registerInDevDirectory has already logged that it is undiscoverable.
		// Reporting success here instead would set published=true and print
		// "co-located consumers will read these tables locally" — with an empty
		// slug — directly contradicting the line above it, for a module whose
		// row does not exist. Swallow it so that one honest line is the only
		// output, and leave published=false so shutdown skips a pointless
		// DELETE.
		if errors.Is(err, errDevDirectoryNoSlug) {
			return nil
		}
		// A failed beat re-arms the log so the eventual recovery prints.
		m.devDir.announcedPublish.Store(false)
		if ensureErr != nil {
			return fmt.Errorf("%w (after: %v)", err, ensureErr)
		}
		return err
	}
	m.devDir.published.Store(true)
	if !m.devDir.announcedPublish.Swap(true) {
		m.logger.Printf("dev: published %s (%s) to the dependency directory, exposing %v — co-located consumers will read these tables locally", m.config.Slug, m.config.ID, m.registry.ExposedTables())
	}
	return nil
}

// ensureDevDirectoryPublished is the ONE-SHOT lazy self-heal for a boot-time
// publish failure.
//
// It runs on the consumer read path because that is the only in-process event
// left after boot: nothing else in a serving module's lifecycle is guaranteed
// to fire. A module whose boot publish failed is invisible to co-located
// consumers, and before this retry existed it stayed invisible until the
// developer noticed and restarted it by hand.
//
// sync.Once, not a retry loop: the read path must not turn a persistently
// broken local Postgres into a per-request write attempt. One retry converts
// the common transient case (a peer was mid-DDL at boot) into a heal; anything
// worse is the heartbeat's job.
func (m *Module) ensureDevDirectoryPublished(ctx context.Context) {
	if m.devDir.published.Load() {
		return
	}
	m.devDir.republish.Do(func() {
		if err := m.publishDevDirectory(ctx); err != nil {
			m.logger.Printf("dev: dependency directory re-publish failed; this module stays invisible to co-located consumers until it restarts: %v", err)
		}
	})
}

// stopDevDirectoryLease ends the heartbeat and drops this module's row.
//
// Best-effort by construction, and the TTL is the real guarantee: a module
// killed with SIGKILL never runs this at all. What a clean shutdown buys is
// closing the window from devDirectoryTTL down to ~zero for the case that
// actually dominates dev — the CLI restarting a module on a code change, where
// a consumer mid-request would otherwise read a producer that is already gone.
//
// The short standalone timeout matters: Close() is called from defer on the
// developer's exit path, and blocking it on an unreachable Postgres would turn
// a clean shutdown into a hang.
func (m *Module) stopDevDirectoryLease() {
	if m.devDir == nil {
		return
	}
	m.devDir.stopOnce.Do(func() { close(m.devDir.stop) })
	// Wait for the heartbeat to actually exit before touching a pool. Close()
	// disposes the pools immediately after this returns, and a beat still in
	// flight would then be writing through a pool being torn down underneath
	// it — a spurious shutdown error at best, and exactly the kind of thing
	// -race is built to find.
	if m.devDir.started.Load() {
		// Bounded for the same reason the DELETE below is: a beat only observes
		// the stop channel between ticks, so one already in flight against a
		// stalled-but-connected Postgres (paused container, laptop resumed from
		// sleep) runs its full heartbeat-length timeout first. Waiting for that
		// unconditionally would hang Ctrl-C — and the CLI's restart-on-save —
		// for up to devDirectoryHeartbeat. Giving up costs only the row
		// withdrawal, which the TTL already covers.
		select {
		case <-m.devDir.done:
		case <-time.After(2 * time.Second):
			m.logger.Printf("dev: dependency directory heartbeat did not stop in time; leaving %s to expire from the directory within %s", m.config.ID, devDirectoryTTL)
			return
		}
	}
	if !m.devDir.published.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool, release, err := m.resolvePool(ctx)
	if err != nil {
		return
	}
	defer release()
	if _, err := pool.Exec(ctx,
		`DELETE FROM `+devDirectoryTable+` WHERE module_id = $1`, m.config.ID); err != nil {
		m.logger.Printf("dev: could not withdraw %s from the dependency directory (it expires on its own within %s): %v", m.config.ID, devDirectoryTTL, err)
	}
}

// colocatedProducer answers the one question the local plane turns on: is ref a
// producer running in THIS dev session right now?
//
// This is the single place that decides co-location, and it owns both bounds
// that make the answer time-honest: the in-process memo here and the freshness
// filter readDevDirectory applies in SQL. Keeping them together is the point —
// splitting them is how a cache quietly outlives the lease it is caching.
//
// A miss is cached exactly like a hit. Misses are the COMMON case (every
// genuinely remote producer is one), and leaving them uncached would mean the
// producers that gain nothing from this feature pay a directory round trip on
// every single read. An error is never cached — see devDirectoryCached.
func (m *Module) colocatedProducer(ctx context.Context, ref string) (devModuleEntry, bool, error) {
	if v, ok := m.devDir.cache.Load(ref); ok {
		if c := v.(*devDirectoryCached); time.Since(c.at) < devDirectoryCacheTTL {
			return c.entry, c.ok, nil
		}
	}
	entry, ok, err := m.devDir.lookup(ctx, ref)
	if err != nil {
		return devModuleEntry{}, false, err
	}
	now := time.Now()
	// Sweep before storing. The key is the caller-supplied producer ref, and
	// DependencyDB accepts an arbitrary string by design (slug, @owner/slug,
	// m<hex>, dashed UUID), so a module deriving that ref from request data
	// would otherwise grow this map for the life of the session, each entry
	// pinning a devModuleEntry. An expired key that is never looked up again is
	// never replaced, so replacement alone does not bound it. In the normal case
	// the map holds one entry per declared dependency and the scan is free.
	m.devDir.cache.Range(func(k, v any) bool {
		if c, isCached := v.(*devDirectoryCached); isCached && now.Sub(c.at) >= devDirectoryCacheTTL {
			m.devDir.cache.Delete(k)
		}
		return true
	})
	m.devDir.cache.Store(ref, &devDirectoryCached{entry: entry, ok: ok, at: now})
	return entry, ok, nil
}

// readDevDirectory resolves a producer ref to its LIVE directory entry.
//
// ok=false means "no module in this dev session answers to that ref RIGHT NOW"
// — the signal to fall through to the read-exposed proxy, NOT an error. A
// producer that runs only in prod is a legitimate cross-plane read, and so is
// one whose lease has expired.
//
// The freshness predicate is expressed entirely in DB time (now() and the row's
// own now()-written updated_at) rather than by computing a cutoff in Go. Mixing
// the two clocks would make the TTL depend on host-vs-container clock skew,
// which in dev is real and silent.
//
// An AMBIGUOUS ref (more than one LIVE row matches) returns an error rather
// than a guess. Picking one of two producers by row order would route a read to
// whichever module happened to boot first, which is both nondeterministic and
// silently wrong; the caller turns the error into a proxy fallthrough, which is
// at least the behavior that existed before this file. Expiry makes this much
// rarer than it was — a long-dead claimant of the slug is no longer a
// candidate at all.
func (m *Module) readDevDirectory(ctx context.Context, ref string) (devModuleEntry, bool, error) {
	keys := directoryLookupKeys(ref)

	// The RAW pool, deliberately not Module.DB: Module.DB wraps in devGuardFor
	// (whose cross-module check has nothing to say about a directory read) and
	// in placeholderQuerier (whose __MODULE_ID__ substitution has nothing to
	// substitute here). Neither belongs on this path.
	pool, release, err := m.resolvePool(ctx)
	if err != nil {
		return devModuleEntry{}, false, fmt.Errorf("dev directory: open pool: %w", err)
	}
	defer release()

	rows, err := pool.Query(ctx, `
		SELECT module_id, slug, exposes
		  FROM `+devDirectoryTable+`
		 WHERE (slug = ANY($1::text[]) OR module_id = ANY($1::text[]))
		   AND updated_at > now() - make_interval(secs => $2)`,
		keys, devDirectoryTTL.Seconds())
	if err != nil {
		return devModuleEntry{}, false, fmt.Errorf("dev directory: lookup %q: %w", ref, err)
	}
	defer rows.Close()

	var found []devModuleEntry
	for rows.Next() {
		var e devModuleEntry
		var raw []byte
		if err := rows.Scan(&e.ModuleID, &e.Slug, &raw); err != nil {
			return devModuleEntry{}, false, fmt.Errorf("dev directory: scan %q: %w", ref, err)
		}
		if err := json.Unmarshal(raw, &e.Exposes); err != nil {
			return devModuleEntry{}, false, fmt.Errorf("dev directory: decode exposes for %q: %w", e.ModuleID, err)
		}
		if e.Exposes == nil {
			e.Exposes = []string{}
		}
		found = append(found, e)
	}
	if err := rows.Err(); err != nil {
		return devModuleEntry{}, false, fmt.Errorf("dev directory: lookup %q: %w", ref, err)
	}

	switch len(found) {
	case 0:
		return devModuleEntry{}, false, nil
	case 1:
		return found[0], true, nil
	default:
		return devModuleEntry{}, false, fmt.Errorf(
			"mirrorstack: dev dependency directory: producer ref %q matches %d co-located modules — resolve the slug/ID collision",
			ref, len(found))
	}
}

// directoryLookupKeys expands a producer ref into the candidate directory keys.
// parseProducerRef yields a bare slug, an m<32 hex> ID, or a dashed UUID (it
// strips owner and semver but permits hyphens), and DependencyDB's doc promises
// all three forms resolve. The dashed form is normalized to the m<hex> the
// platform mints — the same rule as api-platform's ids.ModuleIDFromUUID,
// mirrored rather than imported because the SDK cannot import api-platform.
//
// This function is the SOLE OWNER of that normalization rule, for both the
// directory LOOKUP and authorizeLocalRead's declaration match. Those two used
// to spell it differently, and the drift was a live false denial: a dependency
// declared as the dashed UUID resolved through the directory (handled=true) and
// was then refused by an authorization check comparing the ref RAW. Anything
// that needs to ask "do these two module refs denote the same module?" must go
// through here.
//
// Never returns an empty slice: the verbatim ref is always a candidate, so a
// caller can pass the result straight to `= ANY($1)` without a length check.
func directoryLookupKeys(ref string) []string {
	keys := []string{ref}
	if lower := strings.ToLower(ref); devDirectoryUUIDPattern.MatchString(lower) {
		keys = append(keys, "m"+strings.ReplaceAll(lower, "-", ""))
	}
	return keys
}
