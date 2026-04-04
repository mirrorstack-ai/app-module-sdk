// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package mirrorstack

import (
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/go-chi/chi/v5"

	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
)

// Config holds the module identity. Passed to Init() or New().
type Config struct {
	ID   string // Unique module identifier (required)
	Name string // Default display name (platform can override)
	Icon string // Default Material icon name (platform can override)
}

// Module is the core SDK instance.
type Module struct {
	config Config
	router *chi.Mux
	logger *log.Logger
}

// New creates a new Module.
func New(cfg Config) (*Module, error) {
	if cfg.ID == "" {
		return nil, errors.New("mirrorstack: Config.ID is required")
	}
	return &Module{
		config: cfg,
		router: chi.NewRouter(),
		logger: log.New(os.Stderr, "mirrorstack: ", log.LstdFlags),
	}, nil
}

func (m *Module) Config() Config   { return m.config }
func (m *Module) Router() *chi.Mux { return m.router }

// Platform registers routes with platform auth scope (owner/admin only).
func (m *Module) Platform(fn func(r chi.Router)) { m.router.Group(fn) }

// Public registers routes with public auth scope (anyone, including anonymous).
func (m *Module) Public(fn func(r chi.Router)) { m.router.Group(fn) }

// Internal registers routes with internal auth scope (platform-to-module only).
func (m *Module) Internal(fn func(r chi.Router)) { m.router.Group(fn) }

// Start auto-detects Lambda vs HTTP and starts serving.
func (m *Module) Start() error {
	if runtime.IsLambda() {
		handler := runtime.NewLambdaHandler(m.router)
		lambda.Start(handler)
		return nil
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	m.logger.Printf("%s module (%s) listening on %s", m.config.Name, m.config.ID, addr)
	if err := http.ListenAndServe(addr, m.router); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Convenience API
// ---------------------------------------------------------------------------

var defaultModule *Module

func mustDefault(caller string) *Module {
	if defaultModule == nil {
		panic("mirrorstack: must call Init() before " + caller + "()")
	}
	return defaultModule
}

// Init creates the default module.
//
//	ms.Init(ms.Config{ID: "media", Name: "Media", Icon: "perm_media"})
//	ms.Platform(func(r chi.Router) { ... })
//	ms.Start()
func Init(cfg Config) error {
	mod, err := New(cfg)
	if err != nil {
		return err
	}
	defaultModule = mod
	return nil
}

// Start starts the default module.
func Start() error {
	return mustDefault("Start").Start()
}

// Platform registers platform-scoped routes on the default module.
func Platform(fn func(r chi.Router)) { mustDefault("Platform").Platform(fn) }

// Public registers public-scoped routes on the default module.
func Public(fn func(r chi.Router)) { mustDefault("Public").Public(fn) }

// Internal registers internal-scoped routes on the default module.
func Internal(fn func(r chi.Router)) { mustDefault("Internal").Internal(fn) }

// DefaultModule returns the default module for advanced use cases.
func DefaultModule() *Module { return defaultModule }
