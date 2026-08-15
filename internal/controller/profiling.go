package controller

import (
	"context"
	"expvar"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"
)

// StartProfiling serves pprof and expvar metrics on a loopback-only listener.
// It is off by default and rejects any non-loopback bind address so the
// profiling surface can never be exposed to the network or the base-path mux.
func (s *Server) StartProfiling(ctx context.Context, listenAddr string) error {
	listenAddr = strings.TrimSpace(listenAddr)
	if listenAddr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("pprof listen address %q: %w", listenAddr, err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("pprof listen address %q must be loopback-only", listenAddr)
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen pprof on %s: %w", listenAddr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/debug/vars", expvar.Handler())
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("pprof server: %v", err)
		}
	}()
	log.Printf("profiling endpoints available on http://%s/debug/pprof/ and /debug/vars", listener.Addr())
	return nil
}
