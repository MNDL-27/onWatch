package tracker

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
	"github.com/onllm-dev/onwatch/internal/store"
)

// AntigravityTracker manages limit detection and prediction for Google AI Studio.
type AntigravityTracker struct {
	store  *store.Store
	logger *slog.Logger

	// Tracking state
	lastAvailable      time.Time
	lastLimitHit       *time.Time
	consecutiveErrors  int
	limitEvents24h     int
	hasData            bool

	// Tier detection
	detectedTier       string // "free", "pro", "ultra", "unknown"
	confidenceScore    float64

	onReset func(quotaName string) // called when service recovers from limit
}

// SetOnReset registers a callback that is invoked when the service recovers.
func (t *AntigravityTracker) SetOnReset(fn func(string)) {
	t.onReset = fn
}

// NewAntigravityTracker creates a new AntigravityTracker.
func NewAntigravityTracker(store *store.Store, logger *slog.Logger) *AntigravityTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &AntigravityTracker{
		store:         store,
		logger:        logger,
		detectedTier:  "unknown",
		lastAvailable: time.Now(),
	}
}

// Process analyzes a snapshot and updates predictions.
func (t *AntigravityTracker) Process(snapshot *api.AntigravitySnapshot) error {
	// Get count of recent limit events
	count, err := t.store.CountAntigravityLimitEvents24h()
	if err != nil {
		return fmt.Errorf("failed to count limit events: %w", err)
	}
	t.limitEvents24h = count
	snapshot.LimitHitCount24h = count

	// Detect tier based on patterns
	t.detectTier(snapshot)
	snapshot.CurrentTier = t.detectedTier

	// Calculate risk level
	snapshot.RiskLevel = t.calculateRiskLevel()

	// Estimate reset time
	estimatedReset := t.estimateResetTime()
	snapshot.EstimatedResetTime = estimatedReset

	// Check for recovery (was limited, now available)
	if snapshot.Available && t.lastLimitHit != nil {
		lockoutDuration := time.Since(*t.lastLimitHit)
		t.logger.Info("Antigravity service recovered",
			"lockout_duration", lockoutDuration,
			"tier", t.detectedTier,
		)
		
		if t.onReset != nil {
			t.onReset("api_availability")
		}
		
		// Record recovery event
		recoveryEvent := &api.AntigravityLimitEvent{
			Timestamp:   time.Now().UTC(),
			EventType:   "recovery",
			DetectedTier: t.detectedTier,
		}
		if _, err := t.store.InsertAntigravityLimitEvent(recoveryEvent); err != nil {
			t.logger.Error("Failed to insert recovery event", "error", err)
		}
		
		t.lastLimitHit = nil
		t.consecutiveErrors = 0
	}

	// Track limit hits
	if !snapshot.Available && (snapshot.Status == "rate_limited" || snapshot.Status == "quota_exceeded") {
		t.lastLimitHit = &snapshot.CapturedAt
		t.consecutiveErrors++
	} else if snapshot.Available {
		t.lastAvailable = snapshot.CapturedAt
		t.consecutiveErrors = 0
	}

	t.hasData = true
	return nil
}

// detectTier attempts to determine the user's tier based on observed patterns.
func (t *AntigravityTracker) detectTier(snapshot *api.AntigravitySnapshot) {
	// If we have a configured tier preference, use it
	if t.detectedTier != "unknown" && t.confidenceScore > 0.8 {
		return
	}

	// Get recent limit events for analysis
	since := time.Now().UTC().Add(-7 * 24 * time.Hour) // Last 7 days
	events, err := t.store.QueryAntigravityLimitEvents(since, time.Now().UTC(), 100)
	if err != nil {
		t.logger.Error("Failed to query limit events for tier detection", "error", err)
		return
	}

	// Count events by type and duration
	var rateLimits, quotaExceeded int
	var totalLockoutDuration time.Duration
	var lockoutCount int

	var lastLockoutStart *time.Time
	for _, event := range events {
		switch event.EventType {
		case "rate_limit":
			rateLimits++
			if lastLockoutStart == nil {
				lastLockoutStart = &event.Timestamp
			}
		case "quota_exceeded":
			quotaExceeded++
			if lastLockoutStart == nil {
				lastLockoutStart = &event.Timestamp
			}
		case "recovery":
			if lastLockoutStart != nil {
				duration := event.Timestamp.Sub(*lastLockoutStart)
				totalLockoutDuration += duration
				lockoutCount++
				lastLockoutStart = nil
			}
		}
	}

	// Detect tier based on patterns
	if lockoutCount > 0 {
		avgLockout := totalLockoutDuration / time.Duration(lockoutCount)
		
		switch {
		case avgLockout > 24*time.Hour:
			// Ultra rarely has multi-day lockouts
			// Pro has 7-10 day lockouts on heavy use
			// Free has variable weekly caps
			if avgLockout > 6*24*time.Hour {
				t.detectedTier = "pro" // Likely pro with heavy use penalty
				t.confidenceScore = 0.7
			} else {
				t.detectedTier = "free"
				t.confidenceScore = 0.6
			}
		case avgLockout < 6*time.Hour:
			// Short lockouts (5-hour cycle) suggest free or pro without heavy use
			if rateLimits > quotaExceeded*2 {
				t.detectedTier = "free"
				t.confidenceScore = 0.6
			} else {
				t.detectedTier = "pro"
				t.confidenceScore = 0.5
			}
		default:
			t.detectedTier = "unknown"
			t.confidenceScore = 0.3
		}
	} else if len(events) == 0 && t.hasData {
		// No limit events but we have data - could be ultra or light usage
		t.detectedTier = "ultra"
		t.confidenceScore = 0.4
	}

	t.logger.Debug("Tier detection updated",
		"tier", t.detectedTier,
		"confidence", t.confidenceScore,
		"lockout_count", lockoutCount,
		"avg_lockout", totalLockoutDuration/time.Duration(max(lockoutCount, 1)),
	)
}

// calculateRiskLevel determines the risk of hitting limits.
func (t *AntigravityTracker) calculateRiskLevel() string {
	// High risk indicators
	if t.limitEvents24h >= 3 {
		return "high" // Multiple hits in 24h suggest approaching weekly cap
	}
	if t.consecutiveErrors >= 2 {
		return "high" // Currently in a lockout
	}
	if t.limitEvents24h >= 1 {
		return "medium" // One hit in 24h
	}
	
	return "low"
}

// estimateResetTime predicts when the service will be available again.
func (t *AntigravityTracker) estimateResetTime() *time.Time {
	if t.lastLimitHit == nil {
		return nil // No limit hit, no reset needed
	}

	var resetAfter time.Duration
	
	switch t.detectedTier {
	case "free":
		// Free tier: 5-hour rolling window, but weekly cap possible
		if t.limitEvents24h >= 2 {
			// Multiple hits suggest weekly cap
			resetAfter = 7 * 24 * time.Hour
		} else {
			resetAfter = 5 * time.Hour
		}
	case "pro":
		// Pro tier: 5-hour normally, 7-10 day on heavy use
		if t.limitEvents24h >= 3 {
			// Heavy use detected
			resetAfter = 8 * 24 * time.Hour // Estimate 8 days
		} else {
			resetAfter = 5 * time.Hour
		}
	case "ultra":
		// Ultra tier: Rare limits, short duration
		resetAfter = 1 * time.Hour
	default:
		// Unknown: Assume standard 5-hour
		resetAfter = 5 * time.Hour
	}

	resetTime := t.lastLimitHit.Add(resetAfter)
	return &resetTime
}

// GetPrediction returns the current prediction for the Antigravity API.
func (t *AntigravityTracker) GetPrediction() *api.AntigravityPrediction {
	prediction := &api.AntigravityPrediction{
		CurrentStatus:  "unknown",
		IsLimited:      false,
		Tier:           t.detectedTier,
		RiskLevel:      t.calculateRiskLevel(),
		Recommendation: "Monitor usage",
	}

	if t.lastLimitHit != nil {
		prediction.LastLimitHit = t.lastLimitHit
		prediction.IsLimited = true
		prediction.CurrentStatus = "limited"
		prediction.EstimatedResetTime = t.estimateResetTime()
		
		if prediction.EstimatedResetTime != nil {
			prediction.LockoutDuration = time.Until(*prediction.EstimatedResetTime)
		}
	} else {
		prediction.CurrentStatus = "available"
	}

	// Calculate burn rate (probes per hour in last 24h)
	if t.hasData {
		prediction.BurnRate = float64(t.limitEvents24h) / 24.0
	}

	// Generate recommendation
	prediction.Recommendation = t.generateRecommendation(prediction)

	return prediction
}

// generateRecommendation provides actionable advice based on the prediction.
func (t *AntigravityTracker) generateRecommendation(prediction *api.AntigravityPrediction) string {
	if prediction.IsLimited {
		if prediction.EstimatedResetTime != nil {
			hoursUntil := time.Until(*prediction.EstimatedResetTime).Hours()
			if hoursUntil < 1 {
				return "Service should recover within the hour. Monitor for availability."
			}
			return fmt.Sprintf("Limit hit. Estimated recovery in ~%.0f hours. Consider reducing usage.", hoursUntil)
		}
		return "Limit hit. Monitor for recovery. Consider reducing API usage."
	}

	switch prediction.RiskLevel {
	case "high":
		return "HIGH RISK: Multiple limit events detected. Reduce usage immediately to avoid lockout."
	case "medium":
		return "Medium risk: One limit event in 24h. Consider spacing out requests."
	case "low":
		if prediction.Tier == "unknown" {
			return "Service available. Tier detection in progress."
		}
		return fmt.Sprintf("Service available (%s tier). Continue monitoring.", prediction.Tier)
	}

	return "Monitor usage"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
