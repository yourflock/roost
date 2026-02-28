// activity.go — Now Watching activity reporting to Flock.
// P13-T06: Now Watching Activity
//
// Reports what a subscriber is watching to the Flock social feed so family
// members can see activity and join watch parties. Rate limited: at most one
// "now watching" report per user every 5 minutes (stored in memory).
package flock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// ReportNowWatching tells Flock that a subscriber has started watching content.
// Rate limited: skips the call if a report was sent for this user in the last 5 minutes.
// On Flock API failure: logs a warning and returns nil (non-blocking).
func (c *FlockClient) ReportNowWatching(ctx context.Context, flockUserID, contentTitle, contentType string) error {
	// Rate limit: skip if last report was < 5 minutes ago
	activityMu.Lock()
	if last, ok := lastActivitySent[flockUserID]; ok && time.Since(last) < 5*time.Minute {
		activityMu.Unlock()
		return nil // silently skip — too soon
	}
	lastActivitySent[flockUserID] = time.Now()
	activityMu.Unlock()

	url := fmt.Sprintf("%s/api/activity/watching", c.baseURL)
	payload, _ := json.Marshal(map[string]string{
		"user_id":      flockUserID,
		"content":      contentTitle,
		"content_type": contentType,
		"event":        "started",
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		log.Printf("[flock] ReportNowWatching: build request error: %v", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := flockServiceToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[flock] ReportNowWatching: Flock API unreachable (user=%s): %v", flockUserID, err)
		return nil // non-blocking — activity reporting is best-effort
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		log.Printf("[flock] ReportNowWatching: non-OK status %d for user=%s", resp.StatusCode, flockUserID)
	}
	return nil
}

// ReportWatchingStopped tells Flock that a subscriber stopped watching.
// On Flock API failure: logs and returns nil (non-blocking).
func (c *FlockClient) ReportWatchingStopped(ctx context.Context, flockUserID string) error {
	// Remove from rate limit map so next watch triggers a fresh report
	activityMu.Lock()
	delete(lastActivitySent, flockUserID)
	activityMu.Unlock()

	url := fmt.Sprintf("%s/api/activity/watching", c.baseURL)
	payload, _ := json.Marshal(map[string]string{
		"user_id": flockUserID,
		"event":   "stopped",
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		log.Printf("[flock] ReportWatchingStopped: build request error: %v", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := flockServiceToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[flock] ReportWatchingStopped: Flock API unreachable (user=%s): %v", flockUserID, err)
		return nil
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}
