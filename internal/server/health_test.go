package server

import (
	"context"
	"net"
	"testing"
	"time"

	healthv1 "github.com/swayrider/protos/health/v1"
)

// =============================================================================
// Ping Tests
// =============================================================================

func TestPing(t *testing.T) {
	h := newTestHealthServer("127.0.0.1", 1, time.Second)
	resp, err := h.Ping(context.Background(), &healthv1.PingRequest{})
	if err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("Ping returned nil response")
	}
}

// =============================================================================
// Check Tests
// =============================================================================

// unreachableAddr allocates a free port, then closes the listener to
// guarantee connections to it are refused.
func unreachableAddr(t *testing.T) (string, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate test port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("failed to close test listener: %v", err)
	}
	return "127.0.0.1", port
}

// reachableAddr starts a listener that accepts and immediately closes any
// connection, so dialing it succeeds. The listener is stopped via t.Cleanup.
func reachableAddr(t *testing.T) (string, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	addr := l.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func TestCheck_UnknownComponent(t *testing.T) {
	host, port := reachableAddr(t)
	h := newTestHealthServer(host, port, time.Second)

	for _, component := range []string{"unknown", "smtp", "database"} {
		t.Run(component, func(t *testing.T) {
			resp, err := h.Check(context.Background(), &healthv1.HealthRequest{Component: component})
			if err != nil {
				t.Fatalf("Check(%q) returned error: %v", component, err)
			}
			if resp.Status != healthv1.HealthResponse_UNKNOWN {
				t.Errorf("Check(%q).Status = %v, want %v", component, resp.Status, healthv1.HealthResponse_UNKNOWN)
			}
		})
	}
}

func TestCheck_ReportsUpWhenSMTPReachable(t *testing.T) {
	host, port := reachableAddr(t)
	h := newTestHealthServer(host, port, time.Second)

	for _, component := range []string{"mail", "MAIL", "health", "HEALTH", ""} {
		t.Run(component, func(t *testing.T) {
			resp, err := h.Check(context.Background(), &healthv1.HealthRequest{Component: component})
			if err != nil {
				t.Fatalf("Check(%q) returned error: %v", component, err)
			}
			if resp.Status != healthv1.HealthResponse_UP {
				t.Errorf("Check(%q).Status = %v, want %v", component, resp.Status, healthv1.HealthResponse_UP)
			}
		})
	}
}

func TestCheck_ReportsDownWhenSMTPUnreachable(t *testing.T) {
	host, port := unreachableAddr(t)
	h := newTestHealthServer(host, port, time.Second)

	resp, err := h.Check(context.Background(), &healthv1.HealthRequest{Component: "mail"})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if resp.Status != healthv1.HealthResponse_DOWN {
		t.Errorf("Check.Status = %v, want %v", resp.Status, healthv1.HealthResponse_DOWN)
	}
}

func TestCheck_CachesProbeResultWithinTTL(t *testing.T) {
	host, port := unreachableAddr(t)
	// Long TTL: the second Check below must reuse the first probe result
	// rather than re-dialing, even though the address stays unreachable.
	h := newTestHealthServer(host, port, time.Hour)

	resp1, err := h.Check(context.Background(), &healthv1.HealthRequest{Component: "mail"})
	if err != nil {
		t.Fatalf("first Check returned error: %v", err)
	}
	if resp1.Status != healthv1.HealthResponse_DOWN {
		t.Fatalf("first Check.Status = %v, want %v", resp1.Status, healthv1.HealthResponse_DOWN)
	}

	// Force the cached result to UP directly, bypassing a real probe, to
	// prove the second Check call returns the cache rather than re-dialing.
	h.mu.Lock()
	h.lastUp = true
	h.mu.Unlock()

	resp2, err := h.Check(context.Background(), &healthv1.HealthRequest{Component: "mail"})
	if err != nil {
		t.Fatalf("second Check returned error: %v", err)
	}
	if resp2.Status != healthv1.HealthResponse_UP {
		t.Errorf("second Check.Status = %v, want %v (expected cached result, not a fresh probe)", resp2.Status, healthv1.HealthResponse_UP)
	}
}

func TestCheck_ReprobesAfterTTLExpires(t *testing.T) {
	host, port := unreachableAddr(t)
	h := newTestHealthServer(host, port, time.Millisecond)

	if _, err := h.Check(context.Background(), &healthv1.HealthRequest{Component: "mail"}); err != nil {
		t.Fatalf("first Check returned error: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	resp, err := h.Check(context.Background(), &healthv1.HealthRequest{Component: "mail"})
	if err != nil {
		t.Fatalf("second Check returned error: %v", err)
	}
	if resp.Status != healthv1.HealthResponse_DOWN {
		t.Errorf("second Check.Status = %v, want %v", resp.Status, healthv1.HealthResponse_DOWN)
	}
}
