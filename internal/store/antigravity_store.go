package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

// AntigravityResetCycle represents an Antigravity limit cycle
type AntigravityResetCycle struct {
	ID         int64
	QuotaType  string
	CycleStart time.Time
	CycleEnd   *time.Time
	NextReset  *time.Time
	PeakValue  int64
	TotalDelta int64
}

// InsertAntigravitySnapshot inserts an Antigravity availability snapshot
func (s *Store) InsertAntigravitySnapshot(snapshot *api.AntigravitySnapshot) (int64, error) {
	var lastLimitHit interface{}
	if snapshot.LastLimitHitAt != nil {
		lastLimitHit = snapshot.LastLimitHitAt.Format(time.RFC3339Nano)
	} else {
		lastLimitHit = nil
	}

	var estimatedReset interface{}
	if snapshot.EstimatedResetTime != nil {
		estimatedReset = snapshot.EstimatedResetTime.Format(time.RFC3339Nano)
	} else {
		estimatedReset = nil
	}

	result, err := s.db.Exec(
		`INSERT INTO antigravity_snapshots
		(provider, captured_at, available, status, latency_ms,
		 last_limit_hit_at, limit_hit_count_24h, estimated_reset_time, current_tier, risk_level)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"antigravity",
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		snapshot.Available,
		snapshot.Status,
		snapshot.Latency.Milliseconds(),
		lastLimitHit,
		snapshot.LimitHitCount24h,
		estimatedReset,
		snapshot.CurrentTier,
		snapshot.RiskLevel,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert antigravity snapshot: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert ID: %w", err)
	}

	return id, nil
}

// QueryLatestAntigravity returns the most recent Antigravity snapshot
func (s *Store) QueryLatestAntigravity() (*api.AntigravitySnapshot, error) {
	var snapshot api.AntigravitySnapshot
	var capturedAt string
	var lastLimitHit sql.NullString
	var estimatedReset sql.NullString

	err := s.db.QueryRow(
		`SELECT id, captured_at, available, status, latency_ms,
		 last_limit_hit_at, limit_hit_count_24h, estimated_reset_time, current_tier, risk_level
		FROM antigravity_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(
		&snapshot.ID, &capturedAt, &snapshot.Available, &snapshot.Status,
		&snapshot.Latency, &lastLimitHit, &snapshot.LimitHitCount24h,
		&estimatedReset, &snapshot.CurrentTier, &snapshot.RiskLevel,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest antigravity: %w", err)
	}

	snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	if lastLimitHit.Valid && lastLimitHit.String != "" {
		t, _ := time.Parse(time.RFC3339Nano, lastLimitHit.String)
		snapshot.LastLimitHitAt = &t
	}
	if estimatedReset.Valid && estimatedReset.String != "" {
		t, _ := time.Parse(time.RFC3339Nano, estimatedReset.String)
		snapshot.EstimatedResetTime = &t
	}

	return &snapshot, nil
}

// QueryAntigravityRange returns Antigravity snapshots within a time range with optional limit.
func (s *Store) QueryAntigravityRange(start, end time.Time, limit ...int) ([]*api.AntigravitySnapshot, error) {
	query := `SELECT id, captured_at, available, status, latency_ms,
		 last_limit_hit_at, limit_hit_count_24h, estimated_reset_time, current_tier, risk_level
		FROM antigravity_snapshots
		WHERE captured_at BETWEEN ? AND ?
		ORDER BY captured_at ASC`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.AntigravitySnapshot
	for rows.Next() {
		var snapshot api.AntigravitySnapshot
		var capturedAt string
		var lastLimitHit sql.NullString
		var estimatedReset sql.NullString

		err := rows.Scan(
			&snapshot.ID, &capturedAt, &snapshot.Available, &snapshot.Status,
			&snapshot.Latency, &lastLimitHit, &snapshot.LimitHitCount24h,
			&estimatedReset, &snapshot.CurrentTier, &snapshot.RiskLevel,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan antigravity snapshot: %w", err)
		}

		snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		if lastLimitHit.Valid && lastLimitHit.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, lastLimitHit.String)
			snapshot.LastLimitHitAt = &t
		}
		if estimatedReset.Valid && estimatedReset.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, estimatedReset.String)
			snapshot.EstimatedResetTime = &t
		}

		snapshots = append(snapshots, &snapshot)
	}

	return snapshots, rows.Err()
}

// InsertAntigravityLimitEvent inserts a limit/quota event
func (s *Store) InsertAntigravityLimitEvent(event *api.AntigravityLimitEvent) (int64, error) {
	var estimatedReset interface{}
	if event.EstimatedResetTime != nil {
		estimatedReset = event.EstimatedResetTime.Format(time.RFC3339Nano)
	} else {
		estimatedReset = nil
	}

	result, err := s.db.Exec(
		`INSERT INTO antigravity_limit_events
		(timestamp, event_type, http_status, error_message, latency_ms, retry_after_sec, detected_tier, estimated_reset_time)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.Timestamp.Format(time.RFC3339Nano),
		event.EventType,
		event.HTTPStatus,
		event.ErrorMessage,
		event.Latency.Milliseconds(),
		int64(event.RetryAfter.Seconds()),
		event.DetectedTier,
		estimatedReset,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert antigravity limit event: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get event ID: %w", err)
	}

	return id, nil
}

// QueryAntigravityLimitEvents returns limit events within a time range
func (s *Store) QueryAntigravityLimitEvents(start, end time.Time, limit ...int) ([]*api.AntigravityLimitEvent, error) {
	query := `SELECT id, timestamp, event_type, http_status, error_message, latency_ms, retry_after_sec, detected_tier, estimated_reset_time
		FROM antigravity_limit_events
		WHERE timestamp BETWEEN ? AND ?
		ORDER BY timestamp DESC`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity limit events: %w", err)
	}
	defer rows.Close()

	var events []*api.AntigravityLimitEvent
	for rows.Next() {
		var event api.AntigravityLimitEvent
		var timestamp string
		var estimatedReset sql.NullString
		var retryAfterSec int64

		err := rows.Scan(
			&event.ID, &timestamp, &event.EventType, &event.HTTPStatus,
			&event.ErrorMessage, &event.Latency, &retryAfterSec,
			&event.DetectedTier, &estimatedReset,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan antigravity limit event: %w", err)
		}

		event.Timestamp, _ = time.Parse(time.RFC3339Nano, timestamp)
		event.RetryAfter = time.Duration(retryAfterSec) * time.Second
		if estimatedReset.Valid && estimatedReset.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, estimatedReset.String)
			event.EstimatedResetTime = &t
		}

		events = append(events, &event)
	}

	return events, rows.Err()
}

// CountAntigravityLimitEvents24h returns the count of limit events in the last 24 hours
func (s *Store) CountAntigravityLimitEvents24h() (int, error) {
	var count int
	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM antigravity_limit_events 
		WHERE timestamp > ? AND event_type IN ('rate_limit', 'quota_exceeded')`,
		since,
	).Scan(&count)
	
	if err != nil {
		return 0, fmt.Errorf("failed to count limit events: %w", err)
	}
	
	return count, nil
}

// GetLastAntigravityLimitEvent returns the most recent limit event
func (s *Store) GetLastAntigravityLimitEvent() (*api.AntigravityLimitEvent, error) {
	var event api.AntigravityLimitEvent
	var timestamp string
	var estimatedReset sql.NullString
	var retryAfterSec int64

	err := s.db.QueryRow(
		`SELECT id, timestamp, event_type, http_status, error_message, latency_ms, retry_after_sec, detected_tier, estimated_reset_time
		FROM antigravity_limit_events
		WHERE event_type IN ('rate_limit', 'quota_exceeded')
		ORDER BY timestamp DESC LIMIT 1`,
	).Scan(
		&event.ID, &timestamp, &event.EventType, &event.HTTPStatus,
		&event.ErrorMessage, &event.Latency, &retryAfterSec,
		&event.DetectedTier, &estimatedReset,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get last limit event: %w", err)
	}

	event.Timestamp, _ = time.Parse(time.RFC3339Nano, timestamp)
	event.RetryAfter = time.Duration(retryAfterSec) * time.Second
	if estimatedReset.Valid && estimatedReset.String != "" {
		t, _ := time.Parse(time.RFC3339Nano, estimatedReset.String)
		event.EstimatedResetTime = &t
	}

	return &event, nil
}

// CreateAntigravityCycle creates a new Antigravity limit cycle
func (s *Store) CreateAntigravityCycle(quotaType string, cycleStart time.Time, nextReset *time.Time) (int64, error) {
	var nextResetValue interface{}
	if nextReset != nil {
		nextResetValue = nextReset.Format(time.RFC3339Nano)
	} else {
		nextResetValue = nil
	}

	result, err := s.db.Exec(
		`INSERT INTO antigravity_reset_cycles (quota_type, cycle_start, next_reset) VALUES (?, ?, ?)`,
		quotaType, cycleStart.Format(time.RFC3339Nano), nextResetValue,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create antigravity cycle: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get cycle ID: %w", err)
	}

	return id, nil
}

// CloseAntigravityCycle closes an Antigravity limit cycle with final stats
func (s *Store) CloseAntigravityCycle(quotaType string, cycleEnd time.Time, peak, delta int64) error {
	_, err := s.db.Exec(
		`UPDATE antigravity_reset_cycles SET cycle_end = ?, peak_value = ?, total_delta = ?
		WHERE quota_type = ? AND cycle_end IS NULL`,
		cycleEnd.Format(time.RFC3339Nano), peak, delta, quotaType,
	)
	if err != nil {
		return fmt.Errorf("failed to close antigravity cycle: %w", err)
	}
	return nil
}

// UpdateAntigravityCycle updates the peak and delta for an active Antigravity cycle
func (s *Store) UpdateAntigravityCycle(quotaType string, peak, delta int64) error {
	_, err := s.db.Exec(
		`UPDATE antigravity_reset_cycles SET peak_value = ?, total_delta = ?
		WHERE quota_type = ? AND cycle_end IS NULL`,
		peak, delta, quotaType,
	)
	if err != nil {
		return fmt.Errorf("failed to update antigravity cycle: %w", err)
	}
	return nil
}

// QueryActiveAntigravityCycle returns the active cycle for an Antigravity quota type
func (s *Store) QueryActiveAntigravityCycle(quotaType string) (*AntigravityResetCycle, error) {
	var cycle AntigravityResetCycle
	var cycleStart string
	var cycleEnd, nextReset sql.NullString

	err := s.db.QueryRow(
		`SELECT id, quota_type, cycle_start, cycle_end, next_reset, peak_value, total_delta
		FROM antigravity_reset_cycles WHERE quota_type = ? AND cycle_end IS NULL`,
		quotaType,
	).Scan(
		&cycle.ID, &cycle.QuotaType, &cycleStart, &cycleEnd, &nextReset, &cycle.PeakValue, &cycle.TotalDelta,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active antigravity cycle: %w", err)
	}

	cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
	if cycleEnd.Valid {
		endTime, _ := time.Parse(time.RFC3339Nano, cycleEnd.String)
		cycle.CycleEnd = &endTime
	}
	if nextReset.Valid {
		resetTime, _ := time.Parse(time.RFC3339Nano, nextReset.String)
		cycle.NextReset = &resetTime
	}

	return &cycle, nil
}

// QueryAntigravityCycleHistory returns completed cycles for an Antigravity quota type with optional limit.
func (s *Store) QueryAntigravityCycleHistory(quotaType string, limit ...int) ([]*AntigravityResetCycle, error) {
	query := `SELECT id, quota_type, cycle_start, cycle_end, next_reset, peak_value, total_delta
		FROM antigravity_reset_cycles WHERE quota_type = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`
	args := []interface{}{quotaType}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*AntigravityResetCycle
	for rows.Next() {
		var cycle AntigravityResetCycle
		var cycleStart, cycleEnd string
		var nextReset sql.NullString

		err := rows.Scan(
			&cycle.ID, &cycle.QuotaType, &cycleStart, &cycleEnd, &nextReset, &cycle.PeakValue, &cycle.TotalDelta,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan antigravity cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		endTime, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &endTime
		if nextReset.Valid {
			resetTime, _ := time.Parse(time.RFC3339Nano, nextReset.String)
			cycle.NextReset = &resetTime
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}
