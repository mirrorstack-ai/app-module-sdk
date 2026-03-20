package handler

// X-MS-* headers injected by the core API proxy.
// Modules read these via context getters. Modules must NOT set these directly.
const (
	HeaderAppID                = "X-MS-App-ID"
	HeaderSchemaName           = "X-MS-Schema-Name"
	HeaderAppPublicID          = "X-MS-App-Public-ID"
	HeaderRequestID            = "X-MS-Request-ID"
	HeaderPlatformUserID       = "X-MS-Platform-User-ID"
	HeaderPlatformUserPublicID = "X-MS-Platform-User-Public-ID"
	HeaderModuleID             = "X-MS-Module-ID"
	HeaderAuthType             = "X-MS-Auth-Type"
	HeaderInternalSecret       = "X-Internal-Secret"
)

// Auth types set by the core API proxy in X-MS-Auth-Type.
// Client auth types (client, client-admin) are defined by the OAuth module.
const (
	AuthTypePlatform  = "platform"
	AuthTypeAnonymous = "anonymous"
	AuthTypeInternal  = "internal"
)
