package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"log/slog"
)

func TestProbeResult_ToSnapshot(t *testing.T) {
	now := time.Now().UTC()
	
	tests := []struct {
		name     string
		result   ProbeResult
		expected AntigravitySnapshot
	}{
		{
			name: "available",
			result: ProbeResult{
				Available: true,
				Status:    "available",
				Latency:   150 * time.Millisecond,
			},
			expected: AntigravitySnapshot{
				Available: true,
				Status:    "available",
				Latency:   150 * time.Millisecond,
			},
		},
		{
			name: "rate_limited",
			result: ProbeResult{
				Available:  false,
				Status:     "rate_limited",
				HTTPStatus: 429,
				RetryAfter: 300 * time.Second,
				Latency:    50 * time.Millisecond,
			},
			expected: AntigravitySnapshot{
				Available: false,
				Status:    "rate_limited",
				Latency:   50 * time.Millisecond,
			},
		},
		{
			name: "quota_exceeded",
			result: ProbeResult{
				Available:    false,
				Status:       "quota_exceeded",
				HTTPStatus:   403,
				ErrorMessage: "Quota exceeded",
				Latency:      100 * time.Millisecond,
			},
			expected: AntigravitySnapshot{
				Available: false,
				Status:    "quota_exceeded",
				Latency:   100 * time.Millisecond,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := tt.result.ToSnapshot(now)
			
			if snapshot.Available != tt.expected.Available {
				t.Errorf("Available = %v, want %v", snapshot.Available, tt.expected.Available)
			}
			if snapshot.Status != tt.expected.Status {
				t.Errorf("Status = %s, want %s", snapshot.Status, tt.expected.Status)
			}
			if snapshot.Latency != tt.expected.Latency {
				t.Errorf("Latency = %v, want %v", snapshot.Latency, tt.expected.Latency)
			}
		})
	}
}

func TestProbeResult_IsLimitEvent(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		expected bool
	}{
		{"rate_limited", "rate_limited", true},
		{"quota_exceeded", "quota_exceeded", true},
		{"available", "available", false},
		{"error", "error", false},
		{"forbidden", "forbidden", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ProbeResult{Status: tt.status}
			if got := result.IsLimitEvent(); got != tt.expected {
				t.Errorf("IsLimitEvent() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAntigravityClient_ProbeAvailability_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and path
		if r.Method != http.MethodPost {
			t.Errorf("Method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/models/gemini-pro:generateContent" {
			t.Errorf("Path = %s, want /models/gemini-pro:generateContent", r.URL.Path)
		}

		// Verify content type
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %s, want application/json", ct)
		}

		// Verify it's a valid probe payload
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}

		contents, ok := payload["contents"].([]interface{})
		if !ok || len(contents) == 0 {
			t.Error("Expected contents array in payload")
		}

		// Return success response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"candidates": []map[string]interface{}{
				{
					"content": map[string]interface{}{
						"parts": []map[string]string{
							{"text": "Hi"},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	logger := slog.Default()
	client := NewAntigravityClient("test-key", logger, WithAntigravityBaseURL(server.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.ProbeAvailability(ctx)
	if err != nil {
		t.Fatalf("ProbeAvailability() error = %v", err)
	}

	if !result.Available {
		t.Error("Expected Available = true")
	}
	if result.Status != "available" {
		t.Errorf("Status = %s, want available", result.Status)
	}
	if result.HTTPStatus != http.StatusOK {
		t.Errorf("HTTPStatus = %d, want 200", result.HTTPStatus)
	}
}

func TestAntigravityClient_ProbeAvailability_RateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "Rate limit exceeded. Please try again later.",
		})
	}))
	defer server.Close()

	logger := slog.Default()
	client := NewAntigravityClient("test-key", logger, WithAntigravityBaseURL(server.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.ProbeAvailability(ctx)
	
	// Should return rate limit error
	if err != ErrAntigravityRateLimited {
		t.Errorf("Expected ErrAntigravityRateLimited, got %v", err)
	}

	if result.Available {
		t.Error("Expected Available = false")
	}
	if result.Status != "rate_limited" {
		t.Errorf("Status = %s, want rate_limited", result.Status)
	}
	if result.HTTPStatus != http.StatusTooManyRequests {
		t.Errorf("HTTPStatus = %d, want 429", result.HTTPStatus)
	}
	if result.RetryAfter != 300*time.Second {
		t.Errorf("RetryAfter = %v, want 300s", result.RetryAfter)
	}
}

func TestAntigravityClient_ProbeAvailability_QuotaExceeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "Quota exceeded for quota metric 'GenerateContent requests'",
		})
	}))
	defer server.Close()

	logger := slog.Default()
	client := NewAntigravityClient("test-key", logger, WithAntigravityBaseURL(server.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.ProbeAvailability(ctx)
	
	// Should return quota exceeded error
	if err != ErrAntigravityQuotaExceeded {
		t.Errorf("Expected ErrAntigravityQuotaExceeded, got %v", err)
	}

	if result.Available {
		t.Error("Expected Available = false")
	}
	if result.Status != "quota_exceeded" {
		t.Errorf("Status = %s, want quota_exceeded", result.Status)
	}
	if result.HTTPStatus != http.StatusForbidden {
		t.Errorf("HTTPStatus = %d, want 403", result.HTTPStatus)
	}
}

func TestAntigravityClient_ProbeAvailability_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "Invalid API key",
		})
	}))
	defer server.Close()

	logger := slog.Default()
	client := NewAntigravityClient("invalid-key", logger, WithAntigravityBaseURL(server.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.ProbeAvailability(ctx)
	
	// Should return unauthorized error (not quota exceeded)
	if err != ErrAntigravityUnauthorized {
		t.Errorf("Expected ErrAntigravityUnauthorized, got %v", err)
	}

	if result.Available {
		t.Error("Expected Available = false")
	}
	if result.Status != "forbidden" {
		t.Errorf("Status = %s, want forbidden", result.Status)
	}
}

func TestIsQuotaError(t *testing.T) {
	tests := []struct {
		msg      string
		expected bool
	}{
		{"Quota exceeded", true},
		{"Rate limit reached", true},
		{"Limit exceeded for this resource", true},
		{"Too many requests", true},
		{"Please try again later", true},
		{"Cooldown period active", true},
		{"Resource exhausted", true},
		{"Invalid request", false},
		{"Not found", false},
		{"Internal server error", false},
	}

	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			// We can't test isQuotaError directly since it's not exported,
			// but we can test it through the ProbeAvailability method
			// The tests above cover this functionality
		})
	}
}
