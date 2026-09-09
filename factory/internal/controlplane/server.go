package controlplane

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

func ValidateListenAddress(address string) error {
	_, err := ResolveListenAddress(address)
	return err
}

func ResolveListenAddress(address string) (*net.TCPAddr, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("listen address must include host and port: %w", err)
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return nil, fmt.Errorf("listen host is required")
	}
	portNumber, err := net.LookupPort("tcp", port)
	if err != nil {
		return nil, fmt.Errorf("invalid listen port: %w", err)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return nil, fmt.Errorf("listen host must resolve only to loopback: host is not loopback")
		}
		return &net.TCPAddr{IP: ip, Port: portNumber}, nil
	}
	if !strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return nil, fmt.Errorf("listen host must be a loopback IP or localhost")
	}
	ips, err := lookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("resolve listen host: %w", err)
	}
	if err := validateResolvedLoopback(ips); err != nil {
		return nil, fmt.Errorf("listen host must resolve only to loopback: %w", err)
	}
	return &net.TCPAddr{IP: ips[0], Port: portNumber}, nil
}

func (s *Store) RunSweeper(ctx context.Context, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(s.sweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expired, err := s.SweepExpired(ctx)
			if err != nil {
				logger.Error("lease_sweep_failed", "error_class", "storage_unavailable")
			} else {
				for _, lease := range expired {
					logger.Info("state_change",
						"resource_type", "attempt",
						"resource_id", lease.AttemptID,
						"execution_id", lease.ExecutionID,
						"new_state", "lost",
					)
				}
			}
		}
	}
}

func NewHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
}
