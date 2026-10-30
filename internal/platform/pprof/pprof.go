// Package pprof serves the runtime profiles of net/http/pprof on a separate
// listener (PPROF_ADDR), never on the API port, so the ingress cannot expose
// them. Off unless PPROF_ADDR is set; bind it to loopback and reach it with
// kubectl port-forward (docs/pprof.md).
package pprof

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"
)

// Handler routes /debug/pprof/ on its own mux: importing net/http/pprof
// also registers on http.DefaultServeMux, which the API never serves.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// Start serves Handler on addr until ctx is canceled. An empty addr does
// nothing. A listen failure is logged, not fatal: profiling is optional.
func Start(ctx context.Context, addr string, logger *slog.Logger) {
	if addr == "" {
		return
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: /profile and /trace stream for ?seconds=N.
	}
	go func() {
		logger.Info("pprof listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("pprof listener failed", "addr", addr, "error", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
}
