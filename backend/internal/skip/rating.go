// Package skip provides the scene skip rating inference engine. — SKIP.6.1
//
// Given a content_id's scene data, it computes an inferred content rating
// (G/PG/PG-13/R/NC-17/UNRATED) plus a per-category scene summary count.
// Results are cached per content_id and regenerated when vote_count changes.
package skip

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// Rating is an MPAA-style inferred content rating.
type Rating string

const (
	RatingG       Rating = "G"
	RatingPG      Rating = "PG"
	RatingPG13    Rating = "PG-13"
	RatingR       Rating = "R"
	RatingNC17    Rating = "NC-17"
	RatingUnrated Rating = "UNRATED"
)

// SceneSummary holds per-category approved scene counts.
type SceneSummary struct {
	Sex       int `json:"sex"`
	Nudity    int `json:"nudity"`
	Kissing   int `json:"kissing"`
	Romance   int `json:"romance"`
	Violence  int `json:"violence"`
	Gore      int `json:"gore"`
	Language  int `json:"language"`
	Drugs     int `json:"drugs"`
	JumpScare int `json:"jump_scare"`
	Scary     int `json:"scary"`
}

// RatingResult is the output of InferRating.
type RatingResult struct {
	ContentID      string       `json:"content_id"`
	InferredRating Rating       `json:"inferred_rating"`
	SceneSummary   SceneSummary `json:"scene_summary"`
	ComputedAt     time.Time    `json:"computed_at"`
}

// InferRating computes an inferred content rating for a content_id
// using approved (non-disputed) scene data from the skip_scenes table.
// The result is written back to family_content_rating_overrides.
func InferRating(ctx context.Context, db *sql.DB, contentID string) (*RatingResult, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT category, severity, COUNT(*) AS scene_count
		FROM skip_scenes
		WHERE content_id = $1
		  AND approved = TRUE
		  AND disputed = FALSE
		GROUP BY category, severity
	`, contentID)
	if err != nil {
		return nil, fmt.Errorf("query scenes: %w", err)
	}
	defer rows.Close()

	type row struct {
		category   string
		severity   int
		sceneCount int
	}

	var sceneRows []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.category, &r.severity, &r.sceneCount); err != nil {
			return nil, err
		}
		sceneRows = append(sceneRows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	summary := buildSummary(sceneRows)
	rating := computeRating(summary, sceneRows)

	result := &RatingResult{
		ContentID:      contentID,
		InferredRating: rating,
		SceneSummary:   summary,
		ComputedAt:     time.Now().UTC(),
	}

	// Persist to family_content_rating_overrides (upsert).
	summaryJSON, _ := json.Marshal(summary)
	_, err = db.ExecContext(ctx, `
		INSERT INTO family_content_rating_overrides
		  (content_id, inferred_rating, scene_summary, computed_at)
		VALUES ($1, $2, $3::jsonb, NOW())
		ON CONFLICT (content_id) DO UPDATE SET
		  inferred_rating = $2,
		  scene_summary   = $3::jsonb,
		  computed_at     = NOW()
	`, contentID, string(rating), summaryJSON)
	if err != nil {
		// Non-fatal: log but return the computed result.
		log.Printf("[skip/rating] persist error for %s: %v", contentID, err)
	}

	return result, nil
}

// GetCachedRating reads an existing inferred rating from the DB.
// Returns nil if not yet computed.
func GetCachedRating(ctx context.Context, db *sql.DB, contentID string) (*RatingResult, error) {
	var r RatingResult
	var summaryJSON []byte
	err := db.QueryRowContext(ctx, `
		SELECT content_id, inferred_rating, scene_summary, computed_at
		FROM family_content_rating_overrides
		WHERE content_id = $1
	`, contentID).Scan(&r.ContentID, &r.InferredRating, &summaryJSON, &r.ComputedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(summaryJSON, &r.SceneSummary); err != nil {
		return nil, err
	}
	return &r, nil
}

// ── internal helpers ──────────────────────────────────────────────────────────

type sceneRow struct {
	category   string
	severity   int
	sceneCount int
}

func buildSummary(rows []struct {
	category   string
	severity   int
	sceneCount int
},
) SceneSummary {
	var s SceneSummary
	for _, r := range rows {
		switch r.category {
		case "sex":        s.Sex += r.sceneCount
		case "nudity":     s.Nudity += r.sceneCount
		case "kissing":    s.Kissing += r.sceneCount
		case "romance":    s.Romance += r.sceneCount
		case "violence":   s.Violence += r.sceneCount
		case "gore":       s.Gore += r.sceneCount
		case "language":   s.Language += r.sceneCount
		case "drugs":      s.Drugs += r.sceneCount
		case "jump_scare": s.JumpScare += r.sceneCount
		case "scary":      s.Scary += r.sceneCount
		}
	}
	return s
}

// computeRating maps scene data to an MPAA-style rating using a heuristic.
//
// Rating ladder (most restrictive first):
//   NC-17: sex scenes (severity >= 4) or gore (severity >= 5)
//   R:     sex (any), nudity (severity >= 3), gore (severity >= 3),
//          violence (severity >= 4), drugs (severity >= 3), language (severity >= 4)
//   PG-13: nudity (severity 1-2), violence (severity 2-3), language (severity 2-3),
//          drugs (severity 1-2), gore (severity 1-2)
//   PG:    kissing, romance, violence (severity 1), language (severity 1), jump_scare, scary
//   G:     nothing above
//
// If no scenes at all → UNRATED (not enough data).
func computeRating(summary SceneSummary, rows []struct {
	category   string
	severity   int
	sceneCount int
},
) Rating {
	if totalScenes(summary) == 0 {
		return RatingUnrated
	}

	maxSeverity := func(category string) int {
		max := 0
		for _, r := range rows {
			if r.category == category && r.severity > max {
				max = r.severity
			}
		}
		return max
	}

	sexMax      := maxSeverity("sex")
	nudityMax   := maxSeverity("nudity")
	goreMax     := maxSeverity("gore")
	violenceMax := maxSeverity("violence")
	langMax     := maxSeverity("language")
	drugsMax    := maxSeverity("drugs")

	// NC-17
	if sexMax >= 4 || goreMax >= 5 {
		return RatingNC17
	}
	// R
	if sexMax >= 1 || nudityMax >= 3 || goreMax >= 3 ||
		violenceMax >= 4 || drugsMax >= 3 || langMax >= 4 {
		return RatingR
	}
	// PG-13
	if nudityMax >= 1 || goreMax >= 1 ||
		violenceMax >= 2 || langMax >= 2 || drugsMax >= 1 {
		return RatingPG13
	}
	// PG
	if summary.Kissing > 0 || summary.Romance > 0 ||
		violenceMax >= 1 || langMax >= 1 ||
		summary.JumpScare > 0 || summary.Scary > 0 {
		return RatingPG
	}
	// G — has scenes but all are very mild (severity 0 or no matched categories above)
	return RatingG
}

func totalScenes(s SceneSummary) int {
	return s.Sex + s.Nudity + s.Kissing + s.Romance +
		s.Violence + s.Gore + s.Language + s.Drugs +
		s.JumpScare + s.Scary
}
