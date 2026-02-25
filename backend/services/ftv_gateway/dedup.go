// dedup.go — Content deduplication engine for the Flock TV gateway.
// Phase FLOCKTV FTV.1.T02: checks whether a canonical_id is already in the shared
// acquisition pool before queuing a new acquisition job. Maintains a demand counter
// per canonical_id for priority-based acquisition scheduling.
//
// Key insight: if family A and family B both request the same movie within the same
// minute, both requests result in exactly one acquisition, not two. The
// acquisition_queue.canonical_id UNIQUE constraint (with partial index on non-terminal
// statuses) enforces this at the DB level; this function adds the pre-check layer.
package ftv_gateway

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// DedupeResult describes the outcome of a content dedup check.
type DedupeResult struct {
	// Available is true if the content is already in the shared pool and ready to stream.
	Available bool
	// Processing is true if another worker is currently acquiring this content.
	Processing bool
	// Queued is true if this call added a new acquisition job to the queue.
	Queued bool
	// Priority is the demand count for this canonical_id (higher = more families want it).
	Priority int
}

// acquisitionQueueJob is the Redis queue message format for content acquisition.
type acquisitionQueueJob struct {
	JobID         string `json:"job_id"`
	CanonicalID   string `json:"canonical_id"`
	ContentType   string `json:"content_type"`
	TargetQuality string `json:"target_quality"`
	Priority      int    `json:"priority"`
}

// CheckAndQueueAcquisition is the dedup + queue entry point for content selection.
// Called synchronously in the selection API request path.
//
// Steps:
//  1. Check acquisition_queue for existing entry.
//  2. If complete → return Available.
//  3. If processing/downloading/transcoding → return Processing.
//  4. If not found or failed → increment demand counter, queue new job, return Queued.
func CheckAndQueueAcquisition(
	ctx context.Context,
	db *pgxpool.Pool,
	rdb *redis.Client,
	familyID, canonicalID, contentType string,
) (DedupeResult, error) {
	if db == nil {
		return DedupeResult{Queued: true, Priority: 1}, nil
	}

	// Check existing acquisition status.
	var status string
	row := db.QueryRow(ctx,
		`SELECT status FROM acquisition_queue WHERE canonical_id = $1 ORDER BY queued_at DESC LIMIT 1`,
		canonicalID,
	)
	scanErr := row.Scan(&status)

	if scanErr == nil {
		switch status {
		case "complete":
			return DedupeResult{Available: true}, nil
		case "downloading", "transcoding":
			return DedupeResult{Processing: true}, nil
		case "queued":
			// Already queued — increment demand but don't add duplicate.
			priority := incrementDemand(ctx, rdb, canonicalID)
			return DedupeResult{Queued: true, Priority: priority}, nil
		// "failed" falls through to re-queue logic below.
		}
	}

	// Content not in pool or previously failed — queue acquisition.
	// Increment demand counter.
	priority := incrementDemand(ctx, rdb, canonicalID)

	// Insert into acquisition_queue (idempotent via partial unique index).
	_, insertErr := db.Exec(ctx, `
		INSERT INTO acquisition_queue (canonical_id, content_type, status, queued_at, updated_at)
		VALUES ($1, $2, 'queued', NOW(), NOW())
		ON CONFLICT (canonical_id) WHERE status NOT IN ('complete', 'failed') DO NOTHING`,
		canonicalID, contentType,
	)
	if insertErr != nil {
		return DedupeResult{}, fmt.Errorf("acquisition queue insert failed: %w", insertErr)
	}

	// Push job to Redis queue.
	if rdb != nil {
		job := acquisitionQueueJob{
			JobID:         fmt.Sprintf("job-%s-%d", canonicalID, priority),
			CanonicalID:   canonicalID,
			ContentType:   contentType,
			TargetQuality: targetQualityFor(contentType),
			Priority:      priority,
		}
		jobJSON, _ := json.Marshal(job)
		_ = rdb.RPush(ctx, "content_acquisition_queue", string(jobJSON)).Err()
	}

	return DedupeResult{Queued: true, Priority: priority}, nil
}

// incrementDemand atomically increments the Redis demand counter for a canonical_id.
// Returns the new count (1 for first request, 2+ for subsequent).
func incrementDemand(ctx context.Context, rdb *redis.Client, canonicalID string) int {
	if rdb == nil {
		return 1
	}
	key := fmt.Sprintf("demand:%s", canonicalID)
	count, err := rdb.Incr(ctx, key).Result()
	if err != nil {
		return 1
	}
	return int(count)
}

// targetQualityFor returns the default target quality for a content type.
func targetQualityFor(contentType string) string {
	switch contentType {
	case "music":
		return "flac"
	case "game", "podcast":
		return "copy"
	default:
		return "1080p"
	}
}
