// regions.go — Region-aware channel filtering for handleLive (P14-T02).
//
// This file adds a helper that enriches the /owl/live response with region-based
// channel filtering based on the subscriber's region_id stored in the subscribers table.
//
// When a subscriber has a region_id set:
//   - Only channels linked to that region via channel_regions are returned
//   - If no channels are explicitly linked to the region, all channels are returned (backward compat)
//
// If the subscriber has no region_id set, all active channels are returned.
package main

import (
	"database/sql"
	"fmt"
	"log"
)

// regionFilter returns either empty string (no filter) or a SQL fragment that
// restricts channel results to those available in the subscriber's region.
//
// It queries the subscribers + regions + channel_regions tables introduced in migration 022.
// If those tables don't exist (older DB), it returns empty string gracefully.
//
// Returns:
//   - regionCode: the subscriber's region code (e.g. "eu") or "" if none
//   - filterSQL: SQL fragment to append as AND clause, e.g.
//     "AND c.id IN (SELECT channel_id FROM channel_regions WHERE region_id = '...')"
//   - args: query arguments to pass to the DB query
//   - argOffset: the next placeholder index (for use in building the full query)
func subscriberRegionFilter(db *sql.DB, subscriberID string, existingArgCount int) (regionCode, filterSQL string, args []interface{}) {
	if subscriberID == "" {
		return "", "", nil
	}

	// Look up subscriber's region_id + region code.
	var regionID sql.NullString
	var code sql.NullString
	err := db.QueryRow(`
		SELECT sub.region_id, reg.code
		FROM subscribers sub
		LEFT JOIN regions reg ON reg.id = sub.region_id
		WHERE sub.id = $1
	`, subscriberID).Scan(&regionID, &code)
	if err != nil {
		// Table may not exist (migration 022 not yet applied) — degrade gracefully.
		if err != sql.ErrNoRows {
			log.Printf("[regions] could not look up subscriber region (migration 022 may not be applied): %v", err)
		}
		return "", "", nil
	}

	if !regionID.Valid || !code.Valid {
		// Subscriber has no region set — return all channels.
		return "", "", nil
	}

	// Check whether any channels are linked to this region.
	var linkedCount int
	_ = db.QueryRow(`
		SELECT COUNT(*) FROM channel_regions WHERE region_id = $1
	`, regionID.String).Scan(&linkedCount)

	if linkedCount == 0 {
		// No channels linked to region yet — return all channels for backward compat.
		return code.String, "", nil
	}

	// Build SQL fragment to filter channels by region.
	filterSQL = fmt.Sprintf(
		" AND c.id IN (SELECT channel_id FROM channel_regions WHERE region_id = $%d)",
		existingArgCount+1,
	)
	args = append(args, regionID.String)
	return code.String, filterSQL, args
}
