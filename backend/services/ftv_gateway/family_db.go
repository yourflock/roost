// family_db.go — Per-family database connection pool management.
// Phase FLOCKTV FTV.1.T03: each Flock TV family has a per-family PostgreSQL container
// (provisioned by DockerProvisioner). This file manages connection pools to those
// per-family DBs, with a sync.Map cache keyed by family_id.
//
// Per-family DBs only contain: content_selections, watch_history, preferences.
// Shared catalog data (acquisition_queue, canonical_channels) lives in the central DB.
package ftv_gateway

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FamilyDBManager caches per-family DB connections.
// Max 40 active connections across all family DBs (density constraint on CX23).
type FamilyDBManager struct {
	centralDB *pgxpool.Pool
	pools     sync.Map // key: family_id (string), value: *pgxpool.Pool
}

// NewFamilyDBManager creates a FamilyDBManager backed by the central Roost DB.
func NewFamilyDBManager(centralDB *pgxpool.Pool) *FamilyDBManager {
	return &FamilyDBManager{centralDB: centralDB}
}

// FamilyDBPool returns a live connection pool for the given family.
// Looks up the family's docker_host and postgres_port from the central DB,
// then connects and caches the pool for reuse.
func (m *FamilyDBManager) FamilyDBPool(ctx context.Context, familyID string) (*pgxpool.Pool, error) {
	// Return cached pool if available.
	if cached, ok := m.pools.Load(familyID); ok {
		pool := cached.(*pgxpool.Pool)
		// Health-check the cached connection.
		if pool.Ping(ctx) == nil {
			return pool, nil
		}
		// Unhealthy — remove from cache and reconnect.
		pool.Close()
		m.pools.Delete(familyID)
	}

	if m.centralDB == nil {
		return nil, fmt.Errorf("central DB not available for family DB lookup")
	}

	// Look up family container connection info.
	var dockerHost string
	var postgresPort int
	err := m.centralDB.QueryRow(ctx,
		`SELECT docker_host, postgres_port FROM family_containers WHERE family_id = $1 AND status = 'active'`,
		familyID,
	).Scan(&dockerHost, &postgresPort)
	if err != nil {
		return nil, fmt.Errorf("family container not found for family_id %s: %w", familyID, err)
	}

	dsn := fmt.Sprintf("postgres://flocktv:auto@%s:%d/flocktv_%s?sslmode=disable",
		dockerHost, postgresPort, sanitizeFamilyID(familyID),
	)

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("family DB config parse failed: %w", err)
	}
	// Tight resource limits: each family DB gets max 2 connections.
	cfg.MaxConns = 2
	cfg.MinConns = 0

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("family DB pool creation failed: %w", err)
	}

	m.pools.Store(familyID, pool)
	return pool, nil
}

// Close closes all cached per-family connection pools.
func (m *FamilyDBManager) Close() {
	m.pools.Range(func(_, val interface{}) bool {
		if pool, ok := val.(*pgxpool.Pool); ok {
			pool.Close()
		}
		return true
	})
}

// sanitizeFamilyID strips hyphens and takes first 12 chars for a safe DB name component.
// Matches the naming convention in docker_provisioner.go.
func sanitizeFamilyID(familyID string) string {
	result := ""
	for _, c := range familyID {
		if c != '-' {
			result += string(c)
		}
	}
	if len(result) > 12 {
		return result[:12]
	}
	return result
}
