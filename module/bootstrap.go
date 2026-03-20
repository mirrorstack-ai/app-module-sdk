package module

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	chiadapter "github.com/awslabs/aws-lambda-go-api-proxy/chi"
	"github.com/go-chi/chi/v5"
)

// StartLambda runs the chi router as an AWS Lambda handler.
func StartLambda(router *chi.Mux) {
	adapter := chiadapter.New(router)
	lambda.Start(adapter.ProxyWithContext)
}

// StartHTTP runs the handler as an HTTP server with graceful shutdown.
// Blocks until SIGTERM/SIGINT is received, then drains in-flight requests.
func StartHTTP(addr string, handler http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", addr)
		errCh <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		log.Printf("received %s, shutting down gracefully...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			return fmt.Errorf("graceful shutdown failed: %w", err)
		}
		log.Printf("server stopped")
		return nil
	}
}

// HealthCheck returns a chi middleware-compatible handler for GET /health.
// Mount this on the router for load balancer and platform health checks.
//
//	r.Get("/health", module.HealthCheck())
func HealthCheck() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
	}
}

// Start detects the runtime and starts as Lambda or HTTP server.
// Automatically mounts /health endpoint.
func Start(router *chi.Mux) {
	router.Get("/health", HealthCheck())

	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		StartLambda(router)
	} else {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		if err := StartHTTP(fmt.Sprintf(":%s", port), router); err != nil {
			log.Fatal(err)
		}
	}
}
