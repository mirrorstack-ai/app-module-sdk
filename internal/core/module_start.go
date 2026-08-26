// Package mirrorstack is the Go SDK for building modules on MirrorStack.
//
// Use Init() + Start() for the convenience API, or New() for testing and advanced use.
package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/mirrorstack-ai/app-module-sdk/auth"
	"github.com/mirrorstack-ai/app-module-sdk/internal/runtime"
	"github.com/mirrorstack-ai/app-module-sdk/internal/taskenv"
)

// This file holds startup and shutdown: the serving mode decision, the one-shot task path, and Close.
//
// Split out of a single 1069-line module.go — the SDK's own version of the
// catch-all this codebase tells module authors not to write.

// advertising a surface it cannot deliver.
func (m *Module) checkUIServable() error {
	if m.config.WebDir != "" {
		return nil
	}
	ui := m.registry.UI()
	if ui == nil || (len(ui.DefaultPages) == 0 && len(ui.Components) == 0) {
		return nil
	}
	return fmt.Errorf(
		"mirrorstack: RegisterUI declared %d page(s) and %d component(s) but Config.WebDir is empty — "+
			"the bundle route GET /__mirrorstack/web/* is not mounted, so the platform will 404 when it "+
			"imports the bundle. Set WebDir (conventionally \"web/dist\") in ms.Init, or drop the RegisterUI call",
		len(ui.DefaultPages), len(ui.Components),
	)
}

// Lambda wins if both env vars are set (they are mutually exclusive in
// production but this ordering is a safety net).
func (m *Module) Start() error {
	if runtime.IsLambda() {
		if err := requireInternalSecret(); err != nil {
			return err
		}
		handler := runtime.NewLambdaHandlerWithTasks(m.router, m.config.ID, m.config.Slug, m.runtimeTaskEntries())
		lambda.Start(handler)
		return nil
	}

	if runtime.IsOneShot() {
		return m.startOneShot()
	}

	if err := m.checkUIServable(); err != nil {
		return err
	}

	if m.devMode {
		if err := m.applyDevMigrations(context.Background()); err != nil {
			return err
		}
	}

	// The contributions store is deliberately NOT created here. It is per-app
	// and Start has no app, so creating it on the way to ListenAndServe put it
	// in the connection's default schema, where no request ever looks for it —
	// and Lambda returns above this line anyway, which is how a deployed host
	// ended up with no store at all. Provisioning is the per-(app, module)
	// lifecycle hook's job (lifecycleProvisioner, db.go); dev additionally
	// creates it per app schema in provisionDevAppSchema, which is what serves
	// the tunnel's schema-less install body.

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	m.logger.Printf("%s module (%s) listening on %s", m.config.Name, m.config.ID, addr)
	if err := http.ListenAndServe(addr, m.router); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (m *Module) startOneShot() error {
	if len(m.taskHandlers) == 0 {
		return errors.New("mirrorstack: no tasks registered via OnTask — nothing to execute")
	}
	brokerURL := os.Getenv(taskenv.BrokerURLVar)
	jobID := os.Getenv(taskenv.JobIDVar)
	attemptID := os.Getenv(taskenv.AttemptIDVar)
	bootstrapToken := os.Getenv(taskenv.BootstrapTokenVar)
	claimFile := os.Getenv(taskenv.ClaimFileVar)
	if brokerURL == "" || jobID == "" || attemptID == "" {
		return errors.New("mirrorstack: one-shot mode requires MS_TASK_BROKER_URL, MS_TASK_JOB_ID, and MS_TASK_ATTEMPT_ID")
	}
	if (bootstrapToken == "") == (claimFile == "") {
		return errors.New("mirrorstack: one-shot mode requires exactly one of MS_TASK_BOOTSTRAP_TOKEN or MS_TASK_CLAIM_FILE")
	}
	if !uuidPattern.MatchString(jobID) || !uuidPattern.MatchString(attemptID) {
		return errors.New("mirrorstack: one-shot job and attempt IDs must be UUIDs")
	}
	broker, err := runtime.NewBrokerClient(brokerURL, nil)
	if err != nil {
		return err
	}
	var preclaimed *runtime.ClaimResponse
	if claimFile != "" {
		claim, err := runtime.LoadClaimFile(claimFile)
		if err != nil {
			return err
		}
		if err := os.Remove(claimFile); err != nil {
			return errors.New("mirrorstack: could not remove consumed task claim file")
		}
		preclaimed = &claim
		if err := os.Unsetenv(taskenv.ClaimFileVar); err != nil {
			return errors.New("mirrorstack: could not clear task claim file environment")
		}
	} else if err := os.Unsetenv(taskenv.BootstrapTokenVar); err != nil {
		return errors.New("mirrorstack: could not clear task bootstrap capability environment")
	}
	handlers := m.runtimeTaskEntries()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	defer m.Close()
	return runtime.RunOneShot(ctx, runtime.OneShotConfig{
		Broker: broker, JobID: jobID, AttemptID: attemptID,
		BootstrapToken: bootstrapToken, Preclaimed: preclaimed,
		ModuleID: m.config.ID, ModuleRef: m.config.Slug, Handlers: handlers,
	})
}

func (m *Module) runtimeTaskEntries() map[string]runtime.TaskEntry {
	handlers := make(map[string]runtime.TaskEntry, len(m.taskHandlers))
	for name, entry := range m.taskHandlers {
		handlers[name] = runtime.TaskEntry{Handler: runtime.TaskHandlerFunc(entry.handler), Timeout: entry.timeout}
	}
	return handlers
}

// requireInternalSecret errors if no platform-secret source is configured —
// used by Module.Start() in Lambda mode to fail init before lambda.Start
// handoff. It checks the full hierarchy (auth.SecretConfigured:
// MS_PLATFORM_TOKEN[_FILE] > MS_INTERNAL_SECRET) rather than a single var name,
// so a Lambda module configured with the preferred MS_PLATFORM_TOKEN key (which
// the auth guards already accept) is not blocked at Start.
func requireInternalSecret() error {
	if !auth.SecretConfigured() {
		return errors.New("mirrorstack: no platform secret set (MS_PLATFORM_TOKEN / MS_INTERNAL_SECRET) — required for platform routes in Lambda mode")
	}
	return nil
}

// Close cleans up resources.
func (m *Module) Close() {
	// Before the pools go: withdrawing this module's dev-directory row needs a
	// connection, and stopping the heartbeat before closing the pool it writes
	// through is what keeps a shutdown from logging a spurious failure.
	m.stopDevDirectoryLease()
	if m.poolCache != nil {
		m.poolCache.Close()
	}
	if m.devDB != nil {
		m.devDB.Close()
	}
	if m.cacheCache != nil {
		m.cacheCache.Close()
	}
	if m.devCache != nil {
		m.devCache.Close()
	}
}
