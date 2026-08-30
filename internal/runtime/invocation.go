package runtime

import (
	"errors"
	"net/http"
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/ids"
	"github.com/mirrorstack-ai/app-module-sdk/internal/actor"
	"github.com/mirrorstack-ai/app-module-sdk/internal/invocationwire"
	"github.com/mirrorstack-ai/app-module-sdk/invocation"
)

const invalidInvocationError = "invalid invocation context"

type lambdaInvocation struct {
	context invocation.Context
	proof   []byte
}

func parseLambdaInvocation(req LambdaRequest, moduleID, moduleSlug string) (lambdaInvocation, bool, error) {
	if len(req.Invocation) == 0 {
		return lambdaInvocation{}, false, nil
	}
	trusted, err := invocationwire.Parse(req.Invocation)
	if err != nil || req.Method != trusted.Request.Method || req.Path != trusted.Request.Path ||
		!invocationwire.BodyMatches(trusted, []byte(req.Body)) ||
		(trusted.Identity.ActorDelegation != "" && !actor.ValidTransportValue(trusted.Identity.ActorDelegation)) {
		return lambdaInvocation{}, true, errors.New(invalidInvocationError)
	}
	if moduleID != "" {
		canonical, ok := ids.CanonicalModuleID(moduleID)
		if !ok || canonical != trusted.Module.ID {
			return lambdaInvocation{}, true, errors.New(invalidInvocationError)
		}
	}
	if moduleSlug != "" && moduleSlug != trusted.Module.Slug {
		return lambdaInvocation{}, true, errors.New(invalidInvocationError)
	}
	for name := range req.Headers {
		if strings.EqualFold(name, invocationwire.Header) {
			return lambdaInvocation{}, true, errors.New(invalidInvocationError)
		}
	}
	if err := invocationwire.ValidateLegacyHeaders(trusted, req.Headers); err != nil || !lambdaTopLevelMatches(req, trusted) {
		return lambdaInvocation{}, true, errors.New(invalidInvocationError)
	}
	return lambdaInvocation{
		context: trusted,
		proof:   append([]byte(nil), req.Invocation...),
	}, true, nil
}

func lambdaTopLevelMatches(req LambdaRequest, trusted invocation.Context) bool {
	return matchesWhenPresent(req.UserID, trusted.Identity.UserID) &&
		matchesWhenPresent(req.AppID, trusted.App.ID) &&
		matchesWhenPresent(req.AppRole, trusted.Identity.AppRole) &&
		matchesWhenPresent(req.AppSchema, trusted.App.Schema) &&
		matchesWhenPresent(req.ActorDelegation, trusted.Identity.ActorDelegation)
}

func matchesWhenPresent(legacy, trusted string) bool {
	return legacy == "" || legacy == trusted
}

func isTypedInvocationWireHeader(name string) bool {
	if strings.EqualFold(name, invocationwire.Header) {
		return true
	}
	for _, legacyName := range invocationwire.LegacyHeaderNames() {
		if strings.EqualFold(name, legacyName) {
			return true
		}
	}
	return false
}

func typedInjectParams(req LambdaRequest, trusted invocation.Context) InjectParams {
	return InjectParams{
		Resources:       req.Resources,
		Dependencies:    req.Dependencies,
		UserID:          trusted.Identity.UserID,
		AppID:           trusted.App.ID,
		AppRole:         trusted.Identity.AppRole,
		AppSchema:       trusted.App.Schema,
		ActorDelegation: trusted.Identity.ActorDelegation,
	}
}

func legacyInjectParams(req LambdaRequest) InjectParams {
	return InjectParams{
		Resources:       req.Resources,
		Dependencies:    req.Dependencies,
		UserID:          req.UserID,
		AppID:           req.AppID,
		AppRole:         req.AppRole,
		AppSchema:       req.AppSchema,
		ActorDelegation: req.ActorDelegation,
	}
}

func invocationBadRequest() LambdaResponse {
	return jsonError(http.StatusBadRequest, invalidInvocationError)
}
