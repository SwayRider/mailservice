// health.go implements the health check endpoint.
//
// The health service provides a simple UP/DOWN status for load balancers
// and orchestration systems to verify the service is running. For "mail"
// (and "health"/""), status reflects real SMTP reachability rather than
// unconditionally reporting UP.

package server

import (
	"context"
	"net"
	"strings"
	"time"

	healthv1 "github.com/swayrider/protos/health/v1"
)

// smtpDialTimeout bounds a single reachability probe of the SMTP server.
const smtpDialTimeout = 3 * time.Second

// Check returns the health status of the specified component.
// Returns UP/DOWN based on SMTP reachability for "mail", "health", or empty
// component name; UNKNOWN otherwise.
func (h *HealthServer) Check(
	ctx context.Context,
	req *healthv1.HealthRequest,
) (*healthv1.HealthResponse, error) {
	switch strings.ToLower(req.Component) {
	case "mail", "health", "":
		status := healthv1.HealthResponse_DOWN
		if h.probeSMTP() {
			status = healthv1.HealthResponse_UP
		}
		return &healthv1.HealthResponse{Status: status}, nil
	default:
		return &healthv1.HealthResponse{
			Status: healthv1.HealthResponse_UNKNOWN,
		}, nil
	}
}

// probeSMTP reports whether the SMTP server is reachable, reusing the last
// probe result while it is younger than h.probeTTL to avoid dialing the SMTP
// server on every health check.
func (h *HealthServer) probeSMTP() bool {
	h.mu.Lock()
	if time.Since(h.lastCheck) < h.probeTTL {
		up := h.lastUp
		h.mu.Unlock()
		return up
	}
	h.mu.Unlock()

	conn, err := net.DialTimeout("tcp", h.smtpAddr, smtpDialTimeout)
	up := err == nil
	if up {
		_ = conn.Close()
	} else {
		h.l.Errorf("SMTP health probe failed: %v", err)
	}

	h.mu.Lock()
	h.lastCheck = time.Now()
	h.lastUp = up
	h.mu.Unlock()

	return up
}
