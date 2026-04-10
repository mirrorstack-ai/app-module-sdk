package runtime

import (
	"context"
	"fmt"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/cache"
	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/storage"
)

// InjectParams holds the trusted values to inject into a request context.
// Used by both the Lambda handler and the task worker — any change here
// applies to both paths automatically.
type InjectParams struct {
	Resources *Resources
	UserID    string
	AppID     string
	AppRole   string
	AppSchema string
}

// validRoles is the set of platform roles the SDK recognizes. Messages
// carrying an unknown role are rejected before credential injection.
var validRoles = map[string]bool{
	auth.RoleAdmin:  true,
	auth.RoleMember: true,
	auth.RoleViewer: true,
	"":              true, // empty is valid (internal/system calls with no user)
}

// InjectResources populates ctx with DB/Cache/Storage credentials, app schema,
// and auth identity from the given params. This is the authoritative gate for
// AppSchema validation — callers do not need to validate separately.
//
// Returns an error if AppSchema is non-empty and does not match the expected
// pattern, or if AppRole is not a recognized platform role.
func InjectResources(ctx context.Context, p InjectParams) (context.Context, error) {
	if p.AppSchema != "" && !schemaPattern.MatchString(p.AppSchema) {
		return ctx, fmt.Errorf("mirrorstack: invalid app schema format %q", p.AppSchema)
	}
	if !validRoles[p.AppRole] {
		return ctx, fmt.Errorf("mirrorstack: unknown app role %q", p.AppRole)
	}

	if p.Resources != nil {
		if p.Resources.DB != nil {
			ctx = db.WithCredential(ctx, *p.Resources.DB)
		}
		if p.Resources.Cache != nil {
			ctx = cache.WithCredential(ctx, *p.Resources.Cache)
		}
		if p.Resources.Storage != nil {
			ctx = storage.WithCredential(ctx, *p.Resources.Storage)
		}
	}
	if p.AppSchema != "" {
		ctx = db.WithSchema(ctx, p.AppSchema)
	}
	if p.UserID != "" || p.AppID != "" || p.AppRole != "" {
		ctx = auth.Set(ctx, auth.Identity{
			UserID:  p.UserID,
			AppID:   p.AppID,
			AppRole: p.AppRole,
		})
	}
	return ctx, nil
}
