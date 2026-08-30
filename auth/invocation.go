package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/db"
	"github.com/mirrorstack-ai/app-module-sdk/ids"
	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
	"github.com/mirrorstack-ai/app-module-sdk/internal/httputil"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationstate"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
	"github.com/mirrorstack-ai/app-module-sdk/invocation"
)

const invalidInvocationMessage = "invalid invocation context"

const maxInvocationBodyBytes = 1 << 20

type invocationBinding struct {
	moduleID   string
	moduleSlug string
}

func resolveInvocationBinding(ctx context.Context, fallback invocationBinding) invocationBinding {
	if fallback.moduleID != "" || fallback.moduleSlug != "" {
		return fallback
	}
	binding, ok := invocationstate.BindingFrom(ctx)
	if !ok {
		return fallback
	}
	return invocationBinding{moduleID: binding.ModuleID, moduleSlug: binding.ModuleSlug}
}

// consumeTrustedInvocation validates and consumes the invocation wire after a
// platform credential has authenticated the request. The returned request
// owns a cloned, stripped header map and a restored body.
func consumeTrustedInvocation(w http.ResponseWriter, r *http.Request, binding invocationBinding) (*http.Request, bool, bool) {
	values, matchingNames := headerValuesEqualFold(r.Header, invocationwire.Header)
	if matchingNames == 0 {
		return r, false, true
	}
	if matchingNames != 1 || len(values) != 1 {
		rejectInvocation(w)
		return r, true, false
	}

	trusted, proof, err := invocationwire.DecodeHeader(values[0])
	if err != nil || !requestMatchesInvocation(r, trusted) || !moduleMatchesInvocation(binding, trusted) ||
		(trusted.Identity.ActorDelegation != "" && !actor.ValidTransportValue(trusted.Identity.ActorDelegation)) {
		rejectInvocation(w)
		return r, true, false
	}
	legacy, ok := collectLegacyHeaders(r.Header)
	if !ok || invocationwire.ValidateLegacyHeaders(trusted, legacy) != nil {
		rejectInvocation(w)
		return r, true, false
	}

	body, err := readAndRestoreBody(r)
	if err != nil || !invocationwire.BodyMatches(trusted, body) {
		rejectInvocation(w)
		return r, true, false
	}

	ctx := invocationwire.WithContextAndProof(r.Context(), trusted, proof)
	ctx = actor.WithoutDelegation(ctx)
	ctx = db.WithSchema(ctx, trusted.App.Schema)
	ctx = Set(ctx, Identity{
		UserID:  trusted.Identity.UserID,
		AppID:   trusted.App.ID,
		AppRole: trusted.Identity.AppRole,
	})
	if trusted.Identity.ActorDelegation != "" {
		ctx = actor.WithPendingDelegation(ctx, trusted.Identity.ActorDelegation)
	}
	clean := r.Clone(ctx)
	clean.Header = strippedInvocationHeaders(r.Header, true)
	return clean, true, true
}

func requestMatchesInvocation(r *http.Request, trusted invocation.Context) bool {
	return r.Method == trusted.Request.Method && r.URL.RequestURI() == trusted.Request.Path
}

func moduleMatchesInvocation(binding invocationBinding, trusted invocation.Context) bool {
	if binding.moduleID != "" {
		canonical, ok := ids.CanonicalModuleID(binding.moduleID)
		if !ok || canonical != trusted.Module.ID {
			return false
		}
	}
	return binding.moduleSlug == "" || binding.moduleSlug == trusted.Module.Slug
}

func readAndRestoreBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInvocationBodyBytes+1))
	if closeErr := r.Body.Close(); err == nil {
		err = closeErr
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > maxInvocationBodyBytes && err == nil {
		err = errors.New("invocation body exceeds transport limit")
	}
	return body, err
}

func collectLegacyHeaders(header http.Header) (map[string]string, bool) {
	legacy := make(map[string]string)
	for actualName, values := range header {
		if !isLegacyInvocationHeader(actualName) {
			continue
		}
		if len(values) != 1 {
			return nil, false
		}
		legacy[actualName] = values[0]
	}
	return legacy, true
}

func headerValuesEqualFold(header http.Header, name string) ([]string, int) {
	var values []string
	matchingNames := 0
	for actualName, actualValues := range header {
		if strings.EqualFold(actualName, name) {
			matchingNames++
			values = append(values, actualValues...)
		}
	}
	return values, matchingNames
}

func isLegacyInvocationHeader(name string) bool {
	for _, legacyName := range invocationwire.LegacyHeaderNames() {
		if strings.EqualFold(name, legacyName) {
			return true
		}
	}
	return false
}

func stripUntrustedInvocation(r *http.Request) *http.Request {
	_, matchingNames := headerValuesEqualFold(r.Header, invocationwire.Header)
	if matchingNames == 0 {
		return r
	}
	clean := r.Clone(r.Context())
	clean.Header = strippedInvocationHeaders(r.Header, false)
	return clean
}

func strippedInvocationHeaders(header http.Header, legacy bool) http.Header {
	clean := header.Clone()
	for actualName := range clean {
		if strings.EqualFold(actualName, invocationwire.Header) || legacy && isLegacyInvocationHeader(actualName) {
			delete(clean, actualName)
		}
	}
	return clean
}

func rejectInvocation(w http.ResponseWriter) {
	httputil.JSON(w, http.StatusBadRequest, httputil.ErrorResponse{Error: invalidInvocationMessage})
}
