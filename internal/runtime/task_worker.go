package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/internal/ids"
	"github.com/mirrorstack-ai/app-module-sdk/meter"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
	"golang.org/x/sync/singleflight"
)

type TaskHandlerFunc func(context.Context, json.RawMessage) error

type TaskEntry struct {
	Handler TaskHandlerFunc
	Timeout time.Duration
}

var taskUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var taskModuleRefPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)

// OneShotConfig contains process-local identity and registered handlers. The
// bootstrap capability is consumed only by Claim and is never placed in the
// handler context.
type OneShotConfig struct {
	Broker         *BrokerClient
	JobID          string
	AttemptID      string
	BootstrapToken string
	Preclaimed     *ClaimResponse
	ModuleID       string
	ModuleRef      string
	// ExpectedClass is set by class-specific runtimes such as Standard Lambda.
	// Generic Heavy/GPU runners leave it empty because the signed claim is the
	// authoritative class selection.
	ExpectedClass  string
	Handlers       map[string]TaskEntry
	HeartbeatEvery time.Duration
}

var errTaskCancelled = errors.New("mirrorstack: task cancelled by broker")

const taskResourceRefreshMargin = 30 * time.Second

type taskResourceManager struct {
	mu           sync.RWMutex
	brokerMu     sync.Mutex
	broker       *BrokerClient
	jobID        string
	attemptID    string
	capability   BrokerCapability
	resources    BrokerResources
	cancel       context.CancelCauseFunc
	refreshGroup singleflight.Group
	terminal     bool // protected by brokerMu
}

func (m *taskResourceManager) token() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.capability.Token
}

func (m *taskResourceManager) rotate(cap *BrokerCapability) error {
	if cap == nil {
		return nil
	}
	if err := validateBrokerCapability(*cap, time.Now()); err != nil {
		return err
	}
	m.mu.Lock()
	m.capability = *cap
	m.mu.Unlock()
	return nil
}

func (m *taskResourceManager) refresh(ctx context.Context, kind string) error {
	if m.resourceUsable(kind, time.Now()) {
		return nil
	}
	_, err, _ := m.refreshGroup.Do("resources:"+kind, func() (any, error) {
		// A prior singleflight leader may have completed before this caller
		// entered Do. Check again before acquiring broker serialization.
		if m.resourceUsable(kind, time.Now()) {
			return nil, nil
		}
		m.brokerMu.Lock()
		defer m.brokerMu.Unlock()
		if m.terminal {
			return nil, errors.New("mirrorstack: task resource requested after terminal callback")
		}
		if m.resourceUsable(kind, time.Now()) {
			return nil, nil
		}
		out, err := m.broker.Refresh(ctx, m.jobID, m.attemptID, m.token(), []string{kind})
		if err != nil {
			m.cancel(fmt.Errorf("task resource refresh failed: %w", err))
			return nil, err
		}
		// A refresh response may rotate the lease even when it also reports
		// cancellation or an invalid/omitted resource. Adopt that authority first
		// so the one terminal callback uses the broker's latest capability.
		if err := m.rotate(out.Capability); err != nil {
			m.cancel(err)
			return nil, err
		}
		if out.Cancelled {
			m.cancel(errTaskCancelled)
			return nil, errTaskCancelled
		}
		if err := m.applyRefresh(kind, out); err != nil {
			m.cancel(err)
			return nil, err
		}
		return nil, nil
	})
	return err
}

func (m *taskResourceManager) resourceUsable(kind string, now time.Time) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	safelyValid := func(expiresAt time.Time) bool {
		return expiresAt.After(now.Add(taskResourceRefreshMargin))
	}
	switch kind {
	case "database":
		current := m.resources.Database
		return current != nil && current.Host != "" && current.Port != 0 && current.Database != "" &&
			current.Username != "" && current.Token != "" && safelyValid(current.ExpiresAt)
	case "cache":
		current := m.resources.Cache
		return current != nil && current.Endpoint != "" && current.Username != "" && current.Token != "" && safelyValid(current.ExpiresAt)
	case "storage":
		current := m.resources.Storage
		return current != nil && current.Bucket != "" && current.Region != "" && current.Prefix != "" && current.CDNBase != "" &&
			current.AccessKeyID != "" && current.SecretAccessKey != "" && current.SessionToken != "" && safelyValid(current.ExpiresAt)
	case "moduleCalls":
		current := m.resources.ModuleCalls
		return current != nil && current.Token != "" && safelyValid(current.ExpiresAt)
	default:
		return false
	}
}

func (m *taskResourceManager) applyRefresh(kind string, out RefreshResponse) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	switch kind {
	case "database":
		if out.Resources.Database == nil {
			return errors.New("mirrorstack: database refresh omitted requested resource")
		}
		current := out.Resources.Database
		if current.Host == "" || current.Port == 0 || current.Database == "" || current.Username == "" || current.Token == "" || current.ExpiresAt.IsZero() || !current.ExpiresAt.After(now) {
			return errors.New("mirrorstack: database refresh returned an incomplete or expired credential")
		}
		if old := m.resources.Database; old != nil &&
			(old.Host != current.Host || old.Port != current.Port || old.Database != current.Database || old.Username != current.Username) {
			return errors.New("mirrorstack: refreshed database credential changed scope")
		}
		m.resources.Database = current
	case "cache":
		if out.Resources.Cache == nil {
			return errors.New("mirrorstack: cache refresh omitted requested resource")
		}
		current := out.Resources.Cache
		if current.Endpoint == "" || current.Username == "" || current.Token == "" || current.ExpiresAt.IsZero() || !current.ExpiresAt.After(now) {
			return errors.New("mirrorstack: cache refresh returned an incomplete or expired credential")
		}
		if old := m.resources.Cache; old != nil && (old.Endpoint != current.Endpoint || old.Username != current.Username) {
			return errors.New("mirrorstack: refreshed cache credential changed scope")
		}
		m.resources.Cache = current
	case "storage":
		if out.Resources.Storage == nil {
			return errors.New("mirrorstack: storage refresh omitted requested resource")
		}
		current := out.Resources.Storage
		if current.Bucket == "" || current.Region == "" || current.Prefix == "" || current.CDNBase == "" || current.AccessKeyID == "" || current.SecretAccessKey == "" || current.SessionToken == "" || current.ExpiresAt.IsZero() || !current.ExpiresAt.After(now) {
			return errors.New("mirrorstack: storage refresh returned an incomplete or expired credential")
		}
		if old := m.resources.Storage; old != nil &&
			(old.Bucket != current.Bucket || old.Region != current.Region || old.Prefix != current.Prefix || old.CDNBase != current.CDNBase) {
			return errors.New("mirrorstack: refreshed storage credential changed scope")
		}
		m.resources.Storage = current
	case "moduleCalls":
		if out.Resources.ModuleCalls == nil {
			return errors.New("mirrorstack: moduleCalls refresh omitted requested resource")
		}
		current := out.Resources.ModuleCalls
		if current.Token == "" || current.ExpiresAt.IsZero() || !current.ExpiresAt.After(now) {
			return errors.New("mirrorstack: moduleCalls refresh returned an incomplete or expired capability")
		}
		m.resources.ModuleCalls = current
	default:
		return errors.New("mirrorstack: unsupported resource refresh kind")
	}
	return nil
}

func (m *taskResourceManager) heartbeat(ctx context.Context) (HeartbeatResponse, error) {
	m.brokerMu.Lock()
	defer m.brokerMu.Unlock()
	out, err := m.broker.Heartbeat(ctx, m.jobID, m.attemptID, m.token())
	if err == nil {
		err = m.rotate(out.Capability)
	}
	return out, err
}

func (m *taskResourceManager) complete(ctx context.Context) error {
	m.brokerMu.Lock()
	defer m.brokerMu.Unlock()
	m.terminal = true
	return m.broker.Complete(ctx, m.jobID, m.attemptID, m.token(), nil)
}

func (m *taskResourceManager) fail(ctx context.Context, code, message string, retryable bool) error {
	m.brokerMu.Lock()
	defer m.brokerMu.Unlock()
	m.terminal = true
	return m.broker.Fail(ctx, m.jobID, m.attemptID, m.token(), code, message, retryable)
}

func (m *taskResourceManager) clear() {
	m.brokerMu.Lock()
	defer m.brokerMu.Unlock()
	m.terminal = true
	m.mu.Lock()
	m.capability = BrokerCapability{}
	m.resources = BrokerResources{}
	m.mu.Unlock()
}

func (m *taskResourceManager) heartbeatDelay(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	const (
		defaultDelay = 30 * time.Second
		minimumDelay = time.Second
		expiryMargin = 15 * time.Second
	)
	delay := defaultDelay
	now := time.Now()
	m.mu.RLock()
	capability := m.capability
	m.mu.RUnlock()
	for _, candidate := range []time.Duration{
		capability.RefreshAfter.Sub(now),
		capability.ExpiresAt.Add(-expiryMargin).Sub(now),
	} {
		if candidate > 0 && candidate < delay {
			delay = candidate
		}
	}
	if (!capability.RefreshAfter.IsZero() && !capability.RefreshAfter.After(now)) ||
		(!capability.ExpiresAt.IsZero() && !capability.ExpiresAt.Add(-expiryMargin).After(now)) {
		return minimumDelay
	}
	return delay
}

func (m *taskResourceManager) database(ctx context.Context) (db.Credential, error) {
	if err := m.refresh(ctx, "database"); err != nil {
		return db.Credential{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.resources.Database == nil {
		return db.Credential{}, errors.New("mirrorstack: broker did not vend database credentials")
	}
	return *m.resources.Database, nil
}

func (m *taskResourceManager) cache(ctx context.Context) (cache.Credential, error) {
	if err := m.refresh(ctx, "cache"); err != nil {
		return cache.Credential{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.resources.Cache == nil {
		return cache.Credential{}, errors.New("mirrorstack: broker did not vend cache credentials")
	}
	return *m.resources.Cache, nil
}

func (m *taskResourceManager) storage(ctx context.Context) (storage.Credential, error) {
	if err := m.refresh(ctx, "storage"); err != nil {
		return storage.Credential{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.resources.Storage == nil {
		return storage.Credential{}, errors.New("mirrorstack: broker did not vend storage credentials")
	}
	return *m.resources.Storage, nil
}

func (m *taskResourceManager) moduleCalls(ctx context.Context) (ModuleCallCapability, error) {
	if err := m.refresh(ctx, "moduleCalls"); err != nil {
		return ModuleCallCapability{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.resources.ModuleCalls == nil || m.resources.ModuleCalls.Token == "" {
		return ModuleCallCapability{}, errors.New("mirrorstack: broker did not vend a module-call capability")
	}
	if m.resources.ModuleCalls.ExpiresAt.IsZero() || !m.resources.ModuleCalls.ExpiresAt.After(time.Now()) {
		return ModuleCallCapability{}, errors.New("mirrorstack: broker vended an expired module-call capability")
	}
	return *m.resources.ModuleCalls, nil
}

type databaseProvider struct{ manager *taskResourceManager }

func (p databaseProvider) Credential(ctx context.Context) (db.Credential, error) {
	return p.manager.database(ctx)
}

type cacheProvider struct{ manager *taskResourceManager }

func (p cacheProvider) Credential(ctx context.Context) (cache.Credential, error) {
	return p.manager.cache(ctx)
}

type storageProvider struct{ manager *taskResourceManager }

func (p storageProvider) Credential(ctx context.Context) (storage.Credential, error) {
	return p.manager.storage(ctx)
}

type moduleCallProvider struct{ manager *taskResourceManager }

func (p moduleCallProvider) ModuleCallCapability(ctx context.Context) (ModuleCallCapability, error) {
	return p.manager.moduleCalls(ctx)
}

func (p moduleCallProvider) ServiceCredential(ctx context.Context) (string, error) {
	capability, err := p.manager.moduleCalls(ctx)
	if err != nil {
		return "", err
	}
	return capability.Token, nil
}

type taskDispatchURLKey struct{}

// WithTaskDispatchURL installs the broker-attested dispatch ingress for a
// one-shot attempt. It is context-scoped so concurrent warm Standard Lambda
// invocations never mutate or inherit process-global routing state.
func WithTaskDispatchURL(ctx context.Context, dispatchURL string) context.Context {
	return context.WithValue(ctx, taskDispatchURLKey{}, dispatchURL)
}

// TaskDispatchURLFrom returns the broker-attested dispatch ingress, if this is
// a one-shot handler context.
func TaskDispatchURLFrom(ctx context.Context) string {
	dispatchURL, _ := ctx.Value(taskDispatchURLKey{}).(string)
	return dispatchURL
}

// RunOneShot claims and executes exactly one attempt, then reports exactly one
// terminal outcome. ErrAttemptAlreadyHandled is returned as nil/no execution.
func RunOneShot(parent context.Context, cfg OneShotConfig) error {
	canonicalModuleID, validModuleID := ids.CanonicalModuleID(cfg.ModuleID)
	if cfg.Broker == nil || cfg.JobID == "" || cfg.AttemptID == "" ||
		!validModuleID || !taskModuleRefPattern.MatchString(cfg.ModuleRef) {
		return errors.New("mirrorstack: incomplete one-shot task configuration")
	}
	if !taskUUIDPattern.MatchString(cfg.JobID) || !taskUUIDPattern.MatchString(cfg.AttemptID) {
		return errors.New("mirrorstack: one-shot job and attempt IDs must be UUIDs")
	}
	if (cfg.Preclaimed == nil) == (cfg.BootstrapToken == "") {
		return errors.New("mirrorstack: one-shot requires exactly one claim source")
	}
	var claim ClaimResponse
	if cfg.Preclaimed != nil {
		claim = *cfg.Preclaimed
		// The preclaim handoff is single-use. Drop the caller-owned duplicate of
		// credentials and payload as soon as ownership moves into this runtime.
		*cfg.Preclaimed = ClaimResponse{}
	} else {
		bootstrapToken := cfg.BootstrapToken
		cfg.BootstrapToken = ""
		var err error
		claim, err = cfg.Broker.Claim(parent, cfg.JobID, cfg.AttemptID, bootstrapToken)
		bootstrapToken = ""
		if errors.Is(err, ErrAttemptAlreadyHandled) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if err := validateClaim(cfg, canonicalModuleID, claim); err != nil {
		// A direct broker response has already passed the claim CAS, so a
		// structurally valid identity/capability can be terminalized as an
		// invalid claim. A preclaimed file is only a local handoff and must fail
		// before making any broker callback if validation rejects it.
		if cfg.Preclaimed == nil && claimIdentityMatches(cfg, canonicalModuleID, claim) && validateBrokerCapability(claim.Capability, time.Now()) == nil {
			terminalCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
			defer cancel()
			if callbackErr := cfg.Broker.Fail(terminalCtx, cfg.JobID, cfg.AttemptID, claim.Capability.Token, "invalid_claim", boundedMessage(err), false); callbackErr != nil {
				return fmt.Errorf("%w; failure callback failed: %v", err, callbackErr)
			}
		}
		return err
	}
	entry := cfg.Handlers[claim.Job.Handler]

	deadline := claim.Job.Deadline
	if entry.Timeout > 0 {
		declared := time.Now().Add(entry.Timeout)
		if deadline.IsZero() || declared.Before(deadline) {
			deadline = declared
		}
	}
	baseCtx, cancelCause := context.WithCancelCause(parent)
	defer cancelCause(nil)
	handlerCtx := baseCtx
	var deadlineCancel context.CancelFunc = func() {}
	if !deadline.IsZero() {
		handlerCtx, deadlineCancel = context.WithDeadline(baseCtx, deadline)
	}
	defer deadlineCancel()

	manager := &taskResourceManager{broker: cfg.Broker, jobID: cfg.JobID, attemptID: cfg.AttemptID, capability: claim.Capability, resources: claim.Resources, cancel: cancelCause}
	claim.Capability = BrokerCapability{}
	claim.Resources = BrokerResources{}
	defer manager.clear()
	handlerCtx = auth.Set(handlerCtx, auth.Identity{AppID: claim.Workload.AppID})
	handlerCtx = withTaskAppRef(handlerCtx, claim.Workload.AppRef)
	handlerCtx = auth.WithPayloadTrust(handlerCtx)
	handlerCtx = WithTaskDispatchURL(handlerCtx, claim.Workload.DispatchURL)
	handlerCtx = db.WithSchema(handlerCtx, claim.Workload.AppSchema)
	handlerCtx = db.WithPrefix(handlerCtx, claim.Workload.ModulePrefix)
	// Providers are installed even when an initial snapshot omits a resource:
	// first use asks the broker for current credentials and fails closed when
	// the resource was not admitted. This is essential in warm Standard Lambda
	// processes, where process-global one-shot detection is intentionally false.
	database := databaseProvider{manager}
	// The singular database credential is the scoped module role used by the
	// normal invocation plane. DB and ModuleDB share the renewable source while
	// retaining distinct search_path/prefix behavior in core.
	handlerCtx = db.WithCredentialProvider(handlerCtx, database)
	handlerCtx = db.WithModuleCredentialProvider(handlerCtx, database)
	handlerCtx = cache.WithCredentialProvider(handlerCtx, cacheProvider{manager})
	handlerCtx = storage.WithCredentialProvider(handlerCtx, storageProvider{manager})
	moduleCalls := moduleCallProvider{manager}
	handlerCtx = WithModuleCallCapabilityProvider(handlerCtx, moduleCalls)
	handlerCtx = meter.WithServiceCredentialProvider(handlerCtx, moduleCalls)
	handlerCtx = meter.WithDispatchURL(handlerCtx, claim.Workload.DispatchURL)

	hbCtx, stopHeartbeat := context.WithCancel(baseCtx)
	var heartbeatWG sync.WaitGroup
	heartbeatWG.Add(1)
	go func() {
		defer heartbeatWG.Done()
		interval := cfg.HeartbeatEvery
		for {
			timer := time.NewTimer(manager.heartbeatDelay(interval))
			select {
			case <-hbCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
				out, err := manager.heartbeat(hbCtx)
				if err != nil {
					cancelCause(fmt.Errorf("task heartbeat failed: %w", err))
					return
				}
				if out.Cancelled {
					cancelCause(errTaskCancelled)
					return
				}
				if !out.Deadline.IsZero() && time.Now().After(out.Deadline) {
					cancelCause(context.DeadlineExceeded)
					return
				}
			}
		}
	}()

	handlerErr, panicked := safeRun(handlerCtx, entry.Handler, claim.Payload)
	stopHeartbeat()
	heartbeatWG.Wait()
	if cause := context.Cause(baseCtx); cause != nil {
		handlerErr = cause
		panicked = false
	} else if handlerCtx.Err() != nil {
		handlerErr = handlerCtx.Err()
		panicked = false
	}

	terminalCtx, terminalCancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
	defer terminalCancel()
	if handlerErr == nil {
		if err := manager.complete(terminalCtx); err != nil {
			return fmt.Errorf("mirrorstack: task completion callback failed: %w", err)
		}
		return nil
	}
	code, retryable := classifyTaskFailure(handlerErr, panicked)
	if err := manager.fail(terminalCtx, code, boundedMessage(handlerErr), retryable); err != nil {
		return fmt.Errorf("mirrorstack: task failure callback failed: %w", err)
	}
	return nil
}

func validateClaim(cfg OneShotConfig, canonicalModuleID string, claim ClaimResponse) error {
	now := time.Now()
	if claim.Version != 1 {
		return errors.New("mirrorstack: task broker claim has unsupported version")
	}
	if !claimIdentityMatches(cfg, canonicalModuleID, claim) {
		return errors.New("mirrorstack: task broker claim identity mismatch")
	}
	entry, ok := cfg.Handlers[claim.Job.Handler]
	if !ok || entry.Handler == nil {
		return fmt.Errorf("mirrorstack: task broker selected unregistered handler %q", claim.Job.Handler)
	}
	if err := validateBrokerCapability(claim.Capability, now); err != nil {
		return err
	}
	if claim.Job.Deadline.IsZero() || !claim.Job.Deadline.After(now) ||
		claim.Workload.AppID == "" || claim.Workload.AppRef == "" || claim.Workload.ModuleID == "" ||
		claim.Workload.VersionID == "" || claim.Workload.Version == "" || claim.Workload.ModulePrefix == "" ||
		claim.Workload.AppSchema == "" || !AppSchemaPattern.MatchString(claim.Workload.AppSchema) ||
		(claim.Workload.Class != "standard" && claim.Workload.Class != "heavy" && claim.Workload.Class != "gpu") {
		return errors.New("mirrorstack: task broker claim is missing required trusted fields")
	}
	if err := validateTaskDispatchURL(claim.Workload.DispatchURL); err != nil {
		return err
	}
	if cfg.ExpectedClass != "" && claim.Workload.Class != cfg.ExpectedClass {
		return fmt.Errorf("mirrorstack: task broker claim selected %q for %q runtime", claim.Workload.Class, cfg.ExpectedClass)
	}
	if len(claim.Payload) != 0 && !json.Valid(claim.Payload) {
		return errors.New("mirrorstack: task broker claim payload is invalid JSON")
	}
	return nil
}

// claimIdentityMatches compares the claim against the module's IDENTITY, which
// is the canonical module UUID. The slug (ModuleRef) is a mutable display label
// — module slug rename is a supported operation — so asserting equality on it
// would make every in-flight task of a renamed module fail identity validation,
// and fail it in the one branch that also skips the terminal failure callback:
// the job would hang until the watchdog expired it, on every retry.
func claimIdentityMatches(cfg OneShotConfig, canonicalModuleID string, claim ClaimResponse) bool {
	return claim.Version == 1 && claim.Job.ID == cfg.JobID && claim.Job.AttemptID == cfg.AttemptID &&
		claim.Workload.ModuleID == canonicalModuleID
}

func validateTaskDispatchURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimRight(u.Path, "/") != u.Path {
		return errors.New("mirrorstack: task broker claim has an invalid dispatch URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	// The production wire is HTTPS-only. Loopback HTTP keeps broker contract
	// tests and an explicit local task-plane simulator possible without opening
	// plaintext routing to a remote host.
	host := u.Hostname()
	if u.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return errors.New("mirrorstack: task broker claim dispatch URL must use HTTPS")
}

func validateBrokerCapability(capability BrokerCapability, now time.Time) error {
	if capability.Token == "" || capability.ExpiresAt.IsZero() || !capability.ExpiresAt.After(now) {
		return errors.New("mirrorstack: task broker capability is missing or expired")
	}
	if !capability.RefreshAfter.IsZero() && capability.RefreshAfter.After(capability.ExpiresAt) {
		return errors.New("mirrorstack: task broker capability refresh time exceeds its expiry")
	}
	return nil
}

func safeRun(ctx context.Context, handler TaskHandlerFunc, payload json.RawMessage) (err error, panicked bool) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("task handler panicked: %v", value)
			panicked = true
		}
	}()
	return handler(ctx, payload), false
}

func classifyTaskFailure(err error, panicked bool) (string, bool) {
	if panicked {
		return "handler_panic", true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded", false
	}
	if errors.Is(err, errTaskCancelled) {
		return "cancelled", false
	}
	if errors.Is(err, context.Canceled) {
		return "interrupted", true
	}
	var policy interface{ Retryable() bool }
	if errors.As(err, &policy) && !policy.Retryable() {
		return "permanent_error", false
	}
	return "handler_error", true
}

func boundedMessage(err error) string {
	const max = 2048
	message := err.Error()
	if len(message) > max {
		return message[:max]
	}
	return message
}
