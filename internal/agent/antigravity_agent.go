// Package agent provides the background polling agent for onWatch.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/notify"
	"github.com/onllm-dev/onwatch/internal/store"
	"github.com/onllm-dev/onwatch/internal/tracker"
)

// AntigravityAgent manages the background probe loop for Google AI Studio limit detection.
type AntigravityAgent struct {
	client       *api.AntigravityClient
	store        *store.Store
	tracker      *tracker.AntigravityTracker
	interval     time.Duration
	logger       *slog.Logger
	sm           *SessionManager
	notifier     *notify.NotificationEngine
	pollingCheck func() bool
}

// SetPollingCheck sets a function that is called before each probe.
// If it returns false, the probe is skipped (provider polling disabled).
func (a *AntigravityAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets the notification engine for sending alerts.
func (a *AntigravityAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

// NewAntigravityAgent creates a new AntigravityAgent with the given dependencies.
func NewAntigravityAgent(client *api.AntigravityClient, store *store.Store, tr *tracker.AntigravityTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *AntigravityAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &AntigravityAgent{
		client:   client,
		store:    store,
		tracker:  tr,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}
}

// Run starts the Antigravity agent's probe loop. It probes immediately,
// then continues at the configured interval until the context is cancelled.
func (a *AntigravityAgent) Run(ctx context.Context) error {
	a.logger.Info("antigravity agent started", "interval", a.interval)

	// Ensure any active session is closed on exit
	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("antigravity agent stopped")
	}()

	// Probe immediately on start
	a.probe(ctx)

	// Create ticker for periodic probing
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	// Main probe loop
	for {
		select {
		case <-ticker.C:
			a.probe(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}

// probe performs a single API availability probe and processes the result.
func (a *AntigravityAgent) probe(ctx context.Context) {
	if a.pollingCheck != nil && !a.pollingCheck() {
		return // polling disabled for this provider
	}

	now := time.Now().UTC()
	
	// Perform the probe
	result, err := a.client.ProbeAvailability(ctx)
	if err != nil && !errors.Is(err, api.ErrAntigravityRateLimited) && !errors.Is(err, api.ErrAntigravityQuotaExceeded) {
		// Log non-limit errors but don't stop
		if ctx.Err() != nil {
			return
		}
		a.logger.Error("Failed to probe antigravity API", "error", err)
		
		// Still store the failed probe
		snapshot := &api.AntigravitySnapshot{
			CapturedAt: now,
			Available:  false,
			Status:     "error",
		}
		if result != nil {
			snapshot.Latency = result.Latency
		}
		
		if _, err := a.store.InsertAntigravitySnapshot(snapshot); err != nil {
			a.logger.Error("Failed to insert antigravity snapshot", "error", err)
		}
		return
	}

	// Convert probe result to snapshot
	snapshot := result.ToSnapshot(now)

	// If this was a limit event, record it
	if result.IsLimitEvent() {
		snapshot.LastLimitHitAt = &now
		
		// Create limit event
		event := result.ToLimitEvent(now)
		if _, err := a.store.InsertAntigravityLimitEvent(event); err != nil {
			a.logger.Error("Failed to insert antigravity limit event", "error", err)
		}
		
		// Send notification
		if a.notifier != nil {
			var quotaKey string
			if result.Status == "rate_limited" {
				quotaKey = "rate_limit"
			} else {
				quotaKey = "quota"
			}
			
			a.notifier.Check(notify.QuotaStatus{
				Provider:      "antigravity",
				QuotaKey:      quotaKey,
				ResetOccurred: false,
				Utilization:   100.0, // At limit
			})
		}
	}

	// Store snapshot
	if _, err := a.store.InsertAntigravitySnapshot(snapshot); err != nil {
		a.logger.Error("Failed to insert antigravity snapshot", "error", err)
		return
	}

	// Process with tracker for pattern analysis
	if a.tracker != nil {
		if err := a.tracker.Process(snapshot); err != nil {
			a.logger.Error("antigravity tracker processing failed", "error", err)
		}
	}

	// Report to session manager for usage-based session detection
	if a.sm != nil {
		// Report 1.0 if available, 0.0 if limited
		statusValue := 1.0
		if !result.Available {
			statusValue = 0.0
		}
		a.sm.ReportPoll([]float64{statusValue})
	}

	// Log probe result
	logArgs := []interface{}{
		"available", result.Available,
		"status", result.Status,
		"latency_ms", result.Latency.Milliseconds(),
	}
	
	if result.RetryAfter > 0 {
		logArgs = append(logArgs, "retry_after", result.RetryAfter)
	}
	
	if result.Available {
		a.logger.Info("antigravity probe successful", logArgs...)
	} else {
		a.logger.Warn("antigravity probe failed", logArgs...)
	}
}
