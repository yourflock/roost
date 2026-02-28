// sessions.go — Stream session tracking for the relay service.
// Creates stream_sessions rows on first playlist request and updates bytes_transferred
// on each segment request. Concurrent stream enforcement via an in-memory tracker
// (Redis-backed in production via the ConcurrencyGuard).
package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Session tracks a subscriber's active stream session.
type Session struct {
	ID           uuid.UUID
	SubscriberID uuid.UUID
	ChannelSlug  string
	DeviceID     string
	StartedAt    time.Time
	lastActivity time.Time
}

// Manager manages active stream sessions.
// It creates DB rows on first-access and tracks concurrent stream limits.
type Manager struct {
	db           *sql.DB
	maxStreams    int // default 2
	idleTimeout  time.Duration

	mu       sync.Mutex
	sessions map[string]*Session // key: subscriberID:channelSlug:deviceID
	streams  map[uuid.UUID]map[string]time.Time // subscriberID -> deviceID -> lastActive
}

// NewManager creates a session manager.
// maxStreams is the maximum concurrent streams per subscriber (plan-based; default 2).
func NewManager(db *sql.DB, maxStreams int) *Manager {
	if maxStreams <= 0 {
		maxStreams = 2
	}
	m := &Manager{
		db:          db,
		maxStreams:  maxStreams,
		idleTimeout: 60 * time.Second,
		sessions:    make(map[string]*Session),
		streams:     make(map[uuid.UUID]map[string]time.Time),
	}
	// Background goroutine to expire idle sessions
	go m.expiryLoop()
	return m
}

// OnPlaylistRequest handles a subscriber requesting a channel's m3u8 playlist.
// Creates a new session if this is the first request for this subscriber+channel+device.
// Returns 429-equivalent error if concurrent stream limit is exceeded.
func (m *Manager) OnPlaylistRequest(ctx context.Context, subscriberID uuid.UUID, channelSlug, deviceID string) (*Session, error) {
	key := fmt.Sprintf("%s:%s:%s", subscriberID, channelSlug, deviceID)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Expire stale streams for this subscriber
	m.cleanStreamsLocked(subscriberID)

	// Check concurrent limit — don't count the current device if already active
	active := m.activeStreamsLocked(subscriberID, deviceID)
	if active >= m.maxStreams {
		return nil, fmt.Errorf("concurrent stream limit reached (%d/%d)", active, m.maxStreams)
	}

	// Update stream activity tracker
	if m.streams[subscriberID] == nil {
		m.streams[subscriberID] = make(map[string]time.Time)
	}
	m.streams[subscriberID][deviceID] = time.Now()

	// Return existing session if active
	if sess, ok := m.sessions[key]; ok {
		sess.lastActivity = time.Now()
		return sess, nil
	}

	// Create new session in DB
	sess, err := m.createSession(ctx, subscriberID, channelSlug, deviceID)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	m.sessions[key] = sess
	return sess, nil
}

// OnSegmentRequest records bytes transferred for an active session.
func (m *Manager) OnSegmentRequest(subscriberID uuid.UUID, channelSlug, deviceID string, bytes int64) {
	key := fmt.Sprintf("%s:%s:%s", subscriberID, channelSlug, deviceID)

	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[key]
	if !ok {
		return
	}
	sess.lastActivity = time.Now()

	// Update streams activity tracker
	if m.streams[subscriberID] == nil {
		m.streams[subscriberID] = make(map[string]time.Time)
	}
	m.streams[subscriberID][deviceID] = time.Now()

	// Async DB update
	sessID := sess.ID
	go func() {
		_, err := m.db.ExecContext(context.Background(),
			`UPDATE stream_sessions SET bytes_transferred = bytes_transferred + $1 WHERE id = $2`,
			bytes, sessID,
		)
		if err != nil {
			log.Printf("[relay/sessions] bytes update error: %v", err)
		}
	}()
}

// createSession inserts a new stream_sessions row.
func (m *Manager) createSession(ctx context.Context, subscriberID uuid.UUID, channelSlug, deviceID string) (*Session, error) {
	sess := &Session{
		ID:           uuid.New(),
		SubscriberID: subscriberID,
		ChannelSlug:  channelSlug,
		DeviceID:     deviceID,
		StartedAt:    time.Now(),
		lastActivity: time.Now(),
	}

	_, err := m.db.ExecContext(ctx, `
		INSERT INTO stream_sessions (id, subscriber_id, channel_slug, device_tag, started_at, quality)
		VALUES ($1, $2, $3, $4, $5, '720p')
	`, sess.ID, sess.SubscriberID, sess.ChannelSlug, sess.DeviceID, sess.StartedAt)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// expiryLoop periodically closes sessions idle for more than idleTimeout.
func (m *Manager) expiryLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m.expireIdle()
	}
}

func (m *Manager) expireIdle() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for key, sess := range m.sessions {
		if now.Sub(sess.lastActivity) > m.idleTimeout {
			// Mark session ended in DB
			sessID := sess.ID
			go func() {
				m.db.ExecContext(context.Background(),
					`UPDATE stream_sessions SET ended_at = now() WHERE id = $1 AND ended_at IS NULL`,
					sessID,
				)
			}()
			delete(m.sessions, key)

			// Remove from streams tracker
			if devMap, ok := m.streams[sess.SubscriberID]; ok {
				delete(devMap, sess.DeviceID)
			}
		}
	}

	// Also clean stale streams entries
	for subID := range m.streams {
		m.cleanStreamsLocked(subID)
	}
}

// activeStreamsLocked returns the number of active streams for a subscriber,
// excluding the given device (which is the one requesting, so doesn't count against the limit yet).
func (m *Manager) activeStreamsLocked(subscriberID uuid.UUID, excludeDevice string) int {
	devMap, ok := m.streams[subscriberID]
	if !ok {
		return 0
	}
	cutoff := time.Now().Add(-30 * time.Second)
	count := 0
	for dev, last := range devMap {
		if last.After(cutoff) && dev != excludeDevice {
			count++
		}
	}
	return count
}

// cleanStreamsLocked removes stale entries from the streams tracker.
func (m *Manager) cleanStreamsLocked(subscriberID uuid.UUID) {
	devMap, ok := m.streams[subscriberID]
	if !ok {
		return
	}
	cutoff := time.Now().Add(-60 * time.Second)
	for dev, last := range devMap {
		if last.Before(cutoff) {
			delete(devMap, dev)
		}
	}
	if len(devMap) == 0 {
		delete(m.streams, subscriberID)
	}
}

// ActiveStreamCount returns the number of active streams for a subscriber.
func (m *Manager) ActiveStreamCount(subscriberID uuid.UUID) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanStreamsLocked(subscriberID)
	devMap, ok := m.streams[subscriberID]
	if !ok {
		return 0
	}
	cutoff := time.Now().Add(-30 * time.Second)
	count := 0
	for _, last := range devMap {
		if last.After(cutoff) {
			count++
		}
	}
	return count
}
