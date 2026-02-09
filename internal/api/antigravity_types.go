package api

import (
	"time"
)

// ProbeResult represents the result of an API availability probe.
type ProbeResult struct {
	Available          bool
	Status             string // "available", "rate_limited", "quota_exceeded", "error", "forbidden"
	HTTPStatus         int
	RetryAfter         time.Duration
	ErrorMessage       string
	Latency            time.Duration
	QuotaLimitHeader   int64
	QuotaUsageHeader   int64
}

// AntigravityLimitEvent represents a detected limit/quota event.
type AntigravityLimitEvent struct {
	ID         int64
	Timestamp  time.Time
	EventType  string // "rate_limit", "quota_exceeded", "lockout", "recovery", "probe"
	HTTPStatus int
	ErrorMessage string
	Latency    time.Duration
	RetryAfter time.Duration
	DetectedTier string // "free", "pro", "ultra", "unknown"
	EstimatedResetTime *time.Time
}

// AntigravitySnapshot represents the current state of Antigravity API availability.
type AntigravitySnapshot struct {
	ID int64
	
	// Timestamp when captured
	CapturedAt time.Time
	
	// Current status
	Available bool
	Status    string // "available", "rate_limited", "quota_exceeded", "unknown"
	
	// Probe metrics
	Latency time.Duration
	
	// Limit tracking
	LastLimitHitAt *time.Time
	LimitHitCount24h int
	
	// Prediction
	EstimatedResetTime *time.Time
	CurrentTier        string // "free", "pro", "ultra", "unknown"
	RiskLevel          string // "low", "medium", "high"
}

// AntigravityPrediction contains predicted quota/limit information based on patterns.
type AntigravityPrediction struct {
	CurrentStatus      string
	IsLimited          bool
	LastLimitHit       *time.Time
	EstimatedResetTime *time.Time
	LockoutDuration    time.Duration
	Tier               string
	RiskLevel          string
	BurnRate           float64 // probes per hour in last 24h
	Recommendation     string
}

// ToSnapshot creates a snapshot from a probe result.
func (r *ProbeResult) ToSnapshot(capturedAt time.Time) *AntigravitySnapshot {
	snapshot := &AntigravitySnapshot{
		CapturedAt: capturedAt,
		Available:  r.Available,
		Status:     r.Status,
		Latency:    r.Latency,
	}
	
	// Set tier based on patterns (will be refined by tracker)
	snapshot.CurrentTier = "unknown"
	snapshot.RiskLevel = "low"
	
	return snapshot
}

// IsLimitEvent returns true if the probe result indicates a limit was hit.
func (r *ProbeResult) IsLimitEvent() bool {
	return r.Status == "rate_limited" || r.Status == "quota_exceeded"
}

// ToLimitEvent converts a probe result to a limit event.
func (r *ProbeResult) ToLimitEvent(timestamp time.Time) *AntigravityLimitEvent {
	eventType := "probe"
	switch r.Status {
	case "rate_limited":
		eventType = "rate_limit"
	case "quota_exceeded":
		eventType = "quota_exceeded"
	}
	
	return &AntigravityLimitEvent{
		Timestamp:    timestamp,
		EventType:    eventType,
		HTTPStatus:   r.HTTPStatus,
		ErrorMessage: r.ErrorMessage,
		Latency:      r.Latency,
		RetryAfter:   r.RetryAfter,
		DetectedTier: "unknown",
	}
}
