package handler

import (
	"context"
	"net/http"
)

type contextKey int

const (
	ctxMS        contextKey = iota // *msContext — all X-MS-* values in one struct
	ctxExtracted                   // bool sentinel
	ctxLogger                      // *slog.Logger
)

// msContext holds all X-MS-* header values in a single struct.
// Stored as one context.WithValue call to avoid a deep context chain.
type msContext struct {
	appID                string
	schemaName           string
	appPublicID          string
	requestID            string
	platformUserID       string
	platformUserPublicID string
	moduleID             string
	authType             string
}

// ExtractContext is chi middleware that reads X-MS-* headers into request context.
// Must be the first middleware registered on the router.
//
//	r := chi.NewRouter()
//	r.Use(handler.ExtractContext)
func ExtractContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms := &msContext{
			appID:                r.Header.Get(HeaderAppID),
			schemaName:           r.Header.Get(HeaderSchemaName),
			appPublicID:          r.Header.Get(HeaderAppPublicID),
			requestID:            r.Header.Get(HeaderRequestID),
			platformUserID:       r.Header.Get(HeaderPlatformUserID),
			platformUserPublicID: r.Header.Get(HeaderPlatformUserPublicID),
			moduleID:             r.Header.Get(HeaderModuleID),
			authType:             r.Header.Get(HeaderAuthType),
		}
		ctx := r.Context()
		ctx = context.WithValue(ctx, ctxMS, ms)
		ctx = context.WithValue(ctx, ctxExtracted, true)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isContextExtracted returns true if ExtractContext or NewContext has been applied.
func isContextExtracted(ctx context.Context) bool {
	v, _ := ctx.Value(ctxExtracted).(bool)
	return v
}

// ContextParams holds values for building a context outside of HTTP requests.
// Used by ECS tasks, cron jobs, and CLI tools.
// PlatformUserID and PlatformUserPublicID are intentionally omitted;
// non-HTTP callers (ECS tasks, cron jobs) do not have a platform user.
type ContextParams struct {
	AppID       string
	SchemaName  string
	AppPublicID string
	RequestID   string
	ModuleID    string
	AuthType    string
}

// NewContext builds a context with the same values that ExtractContext
// reads from HTTP headers. Use this for non-HTTP callers (ECS tasks,
// cron jobs) that need to call Emit, Call, or WithSchema.
//
//	ctx := handler.NewContext(context.Background(), handler.ContextParams{
//	    AppID:      os.Getenv("APP_ID"),
//	    SchemaName: os.Getenv("SCHEMA_NAME"),
//	    ModuleID:   os.Getenv("MODULE_ID"),
//	    AuthType:   handler.AuthTypeInternal,
//	})
func NewContext(parent context.Context, p ContextParams) context.Context {
	ms := &msContext{
		appID:       p.AppID,
		schemaName:  p.SchemaName,
		appPublicID: p.AppPublicID,
		requestID:   p.RequestID,
		moduleID:    p.ModuleID,
		authType:    p.AuthType,
	}
	ctx := context.WithValue(parent, ctxMS, ms)
	ctx = context.WithValue(ctx, ctxExtracted, true)
	return ctx
}

// --- Getters ---

func getMS(ctx context.Context) *msContext {
	if ms, ok := ctx.Value(ctxMS).(*msContext); ok {
		return ms
	}
	return nil
}

func GetAppID(ctx context.Context) string {
	if ms := getMS(ctx); ms != nil {
		return ms.appID
	}
	return ""
}

func GetSchemaName(ctx context.Context) string {
	if ms := getMS(ctx); ms != nil {
		return ms.schemaName
	}
	return ""
}

func GetAppPublicID(ctx context.Context) string {
	if ms := getMS(ctx); ms != nil {
		return ms.appPublicID
	}
	return ""
}

func GetRequestID(ctx context.Context) string {
	if ms := getMS(ctx); ms != nil {
		return ms.requestID
	}
	return ""
}

func GetPlatformUserID(ctx context.Context) string {
	if ms := getMS(ctx); ms != nil {
		return ms.platformUserID
	}
	return ""
}

func GetPlatformUserPublicID(ctx context.Context) string {
	if ms := getMS(ctx); ms != nil {
		return ms.platformUserPublicID
	}
	return ""
}

func GetModuleID(ctx context.Context) string {
	if ms := getMS(ctx); ms != nil {
		return ms.moduleID
	}
	return ""
}

func GetAuthType(ctx context.Context) string {
	if ms := getMS(ctx); ms != nil {
		return ms.authType
	}
	return ""
}
