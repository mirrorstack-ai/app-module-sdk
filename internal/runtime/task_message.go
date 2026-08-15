package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

type moduleCallCapabilityKey struct{}
type taskAppRefKey struct{}

// ModuleCallCapability is a short-lived, task-attempt-scoped credential for
// authenticated module-to-platform operations.
type ModuleCallCapability struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// ModuleCallCapabilityProvider renews the outbound attempt capability.
type ModuleCallCapabilityProvider interface {
	ModuleCallCapability(context.Context) (ModuleCallCapability, error)
}

func WithModuleCallCapabilityProvider(ctx context.Context, provider ModuleCallCapabilityProvider) context.Context {
	return context.WithValue(ctx, moduleCallCapabilityKey{}, provider)
}

func ModuleCallCapabilityProviderFrom(ctx context.Context) ModuleCallCapabilityProvider {
	provider, _ := ctx.Value(moduleCallCapabilityKey{}).(ModuleCallCapabilityProvider)
	return provider
}

// withTaskAppRef records the broker-resolved app reference used only for
// managed task job routes. Business APIs continue to see the immutable AppID
// through auth.Identity.
func withTaskAppRef(ctx context.Context, appRef string) context.Context {
	return context.WithValue(ctx, taskAppRefKey{}, appRef)
}

// TaskAppRefFrom returns the broker-resolved app route reference for nested
// task enqueue/status/cancel calls.
func TaskAppRefFrom(ctx context.Context) string {
	appRef, _ := ctx.Value(taskAppRefKey{}).(string)
	return appRef
}

// BrokerCapability is the short-lived, attempt-scoped lease capability.
type BrokerCapability struct {
	Token        string    `json:"token"`
	ExpiresAt    time.Time `json:"expiresAt"`
	RefreshAfter time.Time `json:"refreshAfter"`
}

// BrokerResources is the typed renewable resource snapshot returned by claim
// and refresh. ModuleCalls is deliberately opaque and never projected as an
// ambient module/platform secret.
type BrokerResources struct {
	Database    *db.Credential        `json:"database,omitempty"`
	Storage     *storage.Credential   `json:"storage,omitempty"`
	Cache       *cache.Credential     `json:"cache,omitempty"`
	ModuleCalls *ModuleCallCapability `json:"moduleCalls,omitempty"`
}

type BrokerJob struct {
	ID        string    `json:"id"`
	AttemptID string    `json:"attemptId"`
	Handler   string    `json:"handler"`
	Deadline  time.Time `json:"deadline"`
}

type BrokerWorkload struct {
	AppID              string `json:"appId"`
	AppRef             string `json:"appRef"`
	ModuleID           string `json:"moduleId"`
	ModuleRef          string `json:"moduleRef"`
	DispatchURL        string `json:"dispatchUrl"`
	VersionID          string `json:"versionId"`
	Version            string `json:"version"`
	AppSchema          string `json:"appSchema"`
	ModulePrefix       string `json:"modulePrefix"`
	Class              string `json:"class"`
	CPUUnits           int    `json:"cpuUnits"`
	MemoryMB           int    `json:"memoryMb"`
	EphemeralStorageMB int    `json:"ephemeralStorageMb"`
	GPUModel           string `json:"gpuModel"`
	GPUCount           int    `json:"gpuCount"`
}

type BrokerArtifact struct {
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

type ClaimResponse struct {
	Version    int              `json:"v"`
	Job        BrokerJob        `json:"job"`
	Workload   BrokerWorkload   `json:"workload"`
	Payload    json.RawMessage  `json:"payload"`
	Artifact   BrokerArtifact   `json:"artifact"`
	Resources  BrokerResources  `json:"resources"`
	Capability BrokerCapability `json:"capability"`
}

type HeartbeatResponse struct {
	Version    int               `json:"v"`
	Cancelled  bool              `json:"cancelled"`
	Deadline   time.Time         `json:"deadline"`
	Capability *BrokerCapability `json:"capability,omitempty"`
}

type RefreshResponse struct {
	Version    int               `json:"v"`
	Cancelled  bool              `json:"cancelled"`
	Resources  BrokerResources   `json:"resources"`
	Capability *BrokerCapability `json:"capability,omitempty"`
}
