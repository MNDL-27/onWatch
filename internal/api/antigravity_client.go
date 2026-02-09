// Package api provides client for probing Google AI Studio (Antigravity) API availability.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// Custom errors for different failure modes.
var (
	ErrAntigravityUnauthorized    = errors.New("api: antigravity unauthorized - invalid API key")
	ErrAntigravityRateLimited     = errors.New("api: antigravity rate limited")
	ErrAntigravityQuotaExceeded   = errors.New("api: antigravity quota exceeded")
	ErrAntigravityServerError     = errors.New("api: antigravity server error")
	ErrAntigravityNetworkError    = errors.New("api: antigravity network error")
)

// AntigravityClient is a probe client for Google AI Studio API.
type AntigravityClient struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	logger     *slog.Logger
}

// AntigravityOption configures an AntigravityClient.
type AntigravityOption func(*AntigravityClient)

// WithAntigravityBaseURL sets a custom base URL (for testing).
func WithAntigravityBaseURL(url string) AntigravityOption {
	return func(c *AntigravityClient) {
		c.baseURL = url
	}
}

// WithAntigravityTimeout sets a custom timeout (for testing).
func WithAntigravityTimeout(timeout time.Duration) AntigravityOption {
	return func(c *AntigravityClient) {
		c.httpClient.Timeout = timeout
	}
}

// NewAntigravityClient creates a new Antigravity API probe client.
func NewAntigravityClient(apiKey string, logger *slog.Logger, opts ...AntigravityOption) *AntigravityClient {
	client := &AntigravityClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:          1,
				MaxIdleConnsPerHost:   1,
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
		apiKey:  apiKey,
		baseURL: "https://generativelanguage.googleapis.com/v1beta",
		logger:  logger,
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// ProbeResult represents the result of an API availability probe.
type ProbeResult struct {
	Available          bool
	Status             string // "available", "rate_limited", "quota_exceeded", "error"
	HTTPStatus         int
	RetryAfter         time.Duration
	ErrorMessage       string
	Latency            time.Duration
	QuotaLimitHeader   int64
	QuotaUsageHeader   int64
}

// ProbeAvailability makes a minimal request to test if the API is available.
// Uses a tiny payload to minimize quota consumption while detecting limits.
func (c *AntigravityClient) ProbeAvailability(ctx context.Context) (*ProbeResult, error) {
	start := time.Now()
	
	// Build the minimal probe payload
	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{"text": "Hi"},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 1,
			"temperature":     0,
		},
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("antigravity: marshaling probe payload: %w", err)
	}

	// Build URL with API key
	url := fmt.Sprintf("%s/models/gemini-pro:generateContent?key=%s", c.baseURL, c.apiKey)

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(jsonPayload))
	if err != nil {
		return nil, fmt.Errorf("antigravity: creating probe request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "onwatch/1.0")

	c.logger.Debug("probing antigravity availability",
		"url", c.baseURL+"/models/gemini-pro:generateContent",
	)

	resp, err := c.httpClient.Do(req)
	latency := time.Since(start)
	
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.logger.Error("antigravity probe request failed", "error", err, "latency", latency)
		return &ProbeResult{
			Available:    false,
			Status:       "error",
			ErrorMessage: err.Error(),
			Latency:      latency,
		}, fmt.Errorf("%w: %v", ErrAntigravityNetworkError, err)
	}
	defer resp.Body.Close()

	result := &ProbeResult{
		HTTPStatus: resp.StatusCode,
		Latency:    latency,
	}

	// Parse quota headers if present
	if limit := resp.Header.Get("X-Quota-Limit"); limit != "" {
		if n, err := strconv.ParseInt(limit, 10, 64); err == nil {
			result.QuotaLimitHeader = n
		}
	}
	if usage := resp.Header.Get("X-Quota-Usage"); usage != "" {
		if n, err := strconv.ParseInt(usage, 10, 64); err == nil {
			result.QuotaUsageHeader = n
		}
	}

	// Parse Retry-After header
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		if seconds, err := strconv.Atoi(retry); err == nil {
			result.RetryAfter = time.Duration(seconds) * time.Second
		}
	}

	// Handle different response codes
	switch resp.StatusCode {
	case http.StatusOK:
		result.Available = true
		result.Status = "available"
		c.logger.Debug("antigravity probe successful", "latency", latency)
		return result, nil

	case http.StatusTooManyRequests: // 429
		result.Available = false
		result.Status = "rate_limited"
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		result.ErrorMessage = string(body)
		c.logger.Warn("antigravity rate limited", 
			"retry_after", result.RetryAfter, 
			"latency", latency,
			"body", string(body)[:min(len(string(body)), 200)],
		)
		return result, ErrAntigravityRateLimited

	case http.StatusForbidden: // 403
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		bodyStr := string(body)
		result.ErrorMessage = bodyStr
		
		// Check if it's a quota error or auth error
		if isQuotaError(bodyStr) {
			result.Available = false
			result.Status = "quota_exceeded"
			c.logger.Warn("antigravity quota exceeded", 
				"latency", latency,
				"body", bodyStr[:min(len(bodyStr), 200)],
			)
			return result, ErrAntigravityQuotaExceeded
		}
		result.Available = false
		result.Status = "forbidden"
		c.logger.Error("antigravity forbidden", "body", bodyStr[:min(len(bodyStr), 200)])
		return result, ErrAntigravityUnauthorized

	case http.StatusBadRequest: // 400
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		bodyStr := string(body)
		result.ErrorMessage = bodyStr
		
		// Sometimes quota errors come as 400
		if isQuotaError(bodyStr) {
			result.Available = false
			result.Status = "quota_exceeded"
			c.logger.Warn("antigravity quota exceeded (400)", 
				"latency", latency,
				"body", bodyStr[:min(len(bodyStr), 200)],
			)
			return result, ErrAntigravityQuotaExceeded
		}
		result.Available = false
		result.Status = "error"
		c.logger.Error("antigravity bad request", "body", bodyStr[:min(len(bodyStr), 200)])
		return result, fmt.Errorf("antigravity: bad request: %s", bodyStr)

	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		result.Available = false
		result.Status = "error"
		result.ErrorMessage = string(body)
		c.logger.Error("antigravity unexpected response", 
			"status", resp.StatusCode, 
			"body", string(body)[:min(len(string(body)), 200)],
		)
		return result, fmt.Errorf("%w: HTTP %d", ErrAntigravityServerError, resp.StatusCode)
	}
}

// isQuotaError checks if an error message indicates quota/limit issues
func isQuotaError(msg string) bool {
	quotaKeywords := []string{
		"quota",
		"rate limit",
		"exceeded",
		"limit",
		"too many requests",
		"try again",
		"cooldown",
		"capacity",
		"resource exhausted",
	}
	
	lowerMsg := string(bytes.ToLower([]byte(msg)))
	for _, keyword := range quotaKeywords {
		if contains(lowerMsg, keyword) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || 
		(len(s) > 0 && len(substr) > 0 && containsInternal(s, substr)))
}

func containsInternal(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
