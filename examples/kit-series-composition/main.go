package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"time"

	"github.com/jaredjakacky/clientkit"
	"github.com/jaredjakacky/clientkit/httpclient"
	opskit "github.com/jaredjakacky/opskit"
	servekit "github.com/jaredjakacky/servekit"
	workerkit "github.com/jaredjakacky/workerkit"
)

const (
	checkWorkerName = "client_checks"
	adminToken      = "local-dev"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := run(ctx, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer) (returnErr error) {
	var healthChecks atomic.Int32
	dependency := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		healthChecks.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer dependency.Close()

	check := httpclient.DefaultCheckConfig("healthz")
	payments, err := httpclient.New(httpclient.Config{
		Config: clientkit.Config{
			Name:            "payments",
			ReadinessPolicy: clientkit.ReadinessRequired,
		},
		BaseURL: dependency.URL + "/",
		Check:   check,
	})
	if err != nil {
		return fmt.Errorf("construct payments client: %w", err)
	}
	defer payments.CloseIdleConnections()

	clients := clientkit.NewRegistry()
	if err := clients.Register(payments); err != nil {
		return fmt.Errorf("register payments client: %w", err)
	}

	workers, err := workerkit.New(workerkit.Identity{Name: "workers"})
	if err != nil {
		return fmt.Errorf("construct worker runtime: %w", err)
	}
	if err := workers.Register(workerkit.WorkerSpec{
		Name:        checkWorkerName,
		Description: "Refreshes cached Clientkit health.",
		Worker: workerkit.NewCheckGroupLoop(
			clients,
			workerkit.WithCheckRunImmediately(true),
			workerkit.WithCheckInterval(time.Minute),
			workerkit.WithCheckTimeout(time.Second),
		),
	}, workerkit.WithWorkerReadinessContribution(false)); err != nil {
		return fmt.Errorf("register client check worker: %w", err)
	}

	operations := opskit.NewRegistry()
	if err := operations.Register(clients, opskit.Required()); err != nil {
		return fmt.Errorf("register Clientkit with Opskit: %w", err)
	}
	if err := operations.Register(workers, opskit.Required()); err != nil {
		return fmt.Errorf("register Workerkit with Opskit: %w", err)
	}

	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := servekit.New(
		servekit.WithLogger(discardLogger),
		servekit.WithOps(
			operations,
			servekit.WithOpsAdmin(),
			servekit.WithOpsAdminAuthGate(requireAdminToken),
		),
	)
	// Handler is used without Servekit.Run, so the example owns lifecycle
	// readiness explicitly.
	server.SetReady(true)
	handler := server.Handler()

	beforeStatus, _ := request(handler, "/readyz", "")
	if beforeStatus != http.StatusServiceUnavailable {
		return fmt.Errorf("initial /readyz status = %d, want %d", beforeStatus, http.StatusServiceUnavailable)
	}
	if checks := healthChecks.Load(); checks != 0 {
		return fmt.Errorf("initial /readyz performed %d health checks", checks)
	}
	fmt.Fprintf(output, "before readyz_status=%d health_checks=%d\n", beforeStatus, healthChecks.Load())

	started := false
	defer func() {
		if !started {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		returnErr = errors.Join(returnErr, workers.Shutdown(shutdownCtx))
	}()
	started = true
	if err := workers.StartAll(ctx); err != nil {
		return fmt.Errorf("start workers: %w", err)
	}

	if err := waitFor(ctx, func() bool {
		worker, ok := workers.Worker(checkWorkerName)
		return ok && worker.Status.Ready && clients.Readiness(ctx).Ready
	}); err != nil {
		return fmt.Errorf("wait for initial Clientkit check: %w", err)
	}

	checksAfterRefresh := healthChecks.Load()
	if checksAfterRefresh != 1 {
		return fmt.Errorf("health checks after initial refresh = %d, want 1", checksAfterRefresh)
	}
	afterStatus, _ := request(handler, "/readyz", "")
	if afterStatus != http.StatusOK {
		return fmt.Errorf("refreshed /readyz status = %d, want %d", afterStatus, http.StatusOK)
	}
	if checks := healthChecks.Load(); checks != checksAfterRefresh {
		return fmt.Errorf("refreshed /readyz changed health checks from %d to %d", checksAfterRefresh, checks)
	}
	fmt.Fprintf(output, "after readyz_status=%d health_checks=%d\n", afterStatus, healthChecks.Load())

	unauthorizedStatus, _ := request(handler, "/admin/components/clients", "")
	if unauthorizedStatus != http.StatusUnauthorized {
		return fmt.Errorf("unauthorized inspection status = %d, want %d", unauthorizedStatus, http.StatusUnauthorized)
	}
	authorizedStatus, inspection := request(handler, "/admin/components/clients", adminToken)
	if authorizedStatus != http.StatusOK {
		return fmt.Errorf("authorized inspection status = %d, want %d", authorizedStatus, http.StatusOK)
	}
	if !bytes.Contains(inspection, []byte(`"payments"`)) || !bytes.Contains(inspection, []byte(`"http"`)) {
		return errors.New("authorized inspection omitted Clientkit client identity")
	}
	if bytes.Contains(inspection, []byte(dependency.URL)) {
		return errors.New("authorized inspection exposed the dependency URL")
	}
	if checks := healthChecks.Load(); checks != checksAfterRefresh {
		return fmt.Errorf("inspection changed health checks from %d to %d", checksAfterRefresh, checks)
	}
	fmt.Fprintf(
		output,
		"inspection unauthorized_status=%d authorized_status=%d health_checks=%d\n",
		unauthorizedStatus,
		authorizedStatus,
		healthChecks.Load(),
	)

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := workers.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down workers: %w", err)
	}
	started = false
	return nil
}

func requireAdminToken(r *http.Request) error {
	if r.Header.Get("X-Ops-Token") == adminToken {
		return nil
	}
	return servekit.Error(http.StatusUnauthorized, "ops token required", nil)
}

func request(handler http.Handler, target, token string) (int, []byte) {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("X-Ops-Token", token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.Bytes()
}

func waitFor(ctx context.Context, condition func() bool) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		if condition() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
