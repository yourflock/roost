// handlers_sso.go — Internal Flock→Roost SSO API handlers (DB-wired).
// Phase FLOCKTV FTV.0.T04: when a Flock subscriber activates Flock TV, Flock's backend
// calls /internal/flocktv/provision to create a Roost subscriber record and trigger
// Docker container provisioning for the family.
//
// These endpoints are NEVER exposed via public Nginx — internal-only, protected by
// X-Flock-Internal-Secret which must match FLOCK_INTERNAL_SECRET env var.
package flocktv

import (
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
)

// ssoProvisionRequest is the POST /internal/flocktv/provision body.
// Called by Flock backend when a subscriber activates Flock TV.
type ssoProvisionRequest struct {
	FamilyID          string `json:"family_id"`
	UserID            string `json:"user_id"`
	Plan              string `json:"plan"`                // flock_family_tv, roost_standalone
	FlockJWTPublicKey string `json:"flock_jwt_public_key"` // PEM-encoded EC public key
}

// checkInternalSecret validates the X-Flock-Internal-Secret header.
// Returns true if the request is authorized; writes 401 and returns false otherwise.
func checkInternalSecret(w http.ResponseWriter, r *http.Request) bool {
	expected := os.Getenv("FLOCK_INTERNAL_SECRET")
	if expected == "" {
		writeError(w, http.StatusServiceUnavailable, "config_error",
			"FLOCK_INTERNAL_SECRET is not configured on this server")
		return false
	}
	if r.Header.Get("X-Flock-Internal-Secret") != expected {
		writeError(w, http.StatusUnauthorized, "unauthorized",
			"invalid or missing X-Flock-Internal-Secret")
		return false
	}
	return true
}

// handleSSOprovision provisions a new Flock TV subscriber and their Docker container.
// POST /internal/flocktv/provision
func (s *Server) handleSSOprovision(w http.ResponseWriter, r *http.Request) {
	if !checkInternalSecret(w, r) {
		return
	}

	var req ssoProvisionRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.FamilyID == "" || req.UserID == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "family_id and user_id are required")
		return
	}

	validPlans := map[string]bool{
		"flock_family_tv":  true,
		"roost_standalone": true,
		"self_hosted":      true,
	}
	if req.Plan != "" && !validPlans[req.Plan] {
		writeError(w, http.StatusBadRequest, "invalid_plan",
			"plan must be flock_family_tv, roost_standalone, or self_hosted")
		return
	}

	plan := req.Plan
	if plan == "" {
		plan = "flock_family_tv"
	}

	dockerPort := 0
	containerStatus := "provisioned"

	if s.db != nil {
		// Upsert subscriber record with flocktv_tier.
		_, dbErr := s.db.ExecContext(r.Context(), `
			INSERT INTO subscribers (id, email, flocktv_tier, flocktv_active_since, status)
			VALUES ($1, $1 || '@flock.internal', $2, NOW(), 'active')
			ON CONFLICT (id) DO UPDATE
			  SET flocktv_tier = EXCLUDED.flocktv_tier,
			      flocktv_active_since = COALESCE(subscribers.flocktv_active_since, NOW()),
			      status = 'active'`,
			req.FamilyID, plan,
		)
		if dbErr != nil {
			s.logger.Error("sso provision subscriber upsert failed", "error", dbErr.Error(), "family_id", req.FamilyID)
			// Non-fatal — continue with Docker provisioning.
		}

		// Store Flock JWT public key for this family.
		if req.FlockJWTPublicKey != "" {
			_, _ = s.db.ExecContext(r.Context(), `
				INSERT INTO flock_sso_keys (family_id, public_key_pem, algorithm, created_at, active)
				VALUES ($1, $2, 'ES256', NOW(), true)
				ON CONFLICT (family_id) DO UPDATE
				  SET public_key_pem = EXCLUDED.public_key_pem,
				      created_at = NOW(),
				      active = true`,
				req.FamilyID, req.FlockJWTPublicKey,
			)
		}
	}

	// Provision Docker container for this family.
	if s.db != nil {
		provisioner := &DockerProvisioner{
			DockerHost: getEnv("DOCKER_HOST_INTERNAL", "127.0.0.1"),
			BasePort:   6000,
		}
		port, provErr := provisioner.ProvisionFamily(r.Context(), req.FamilyID)
		if provErr != nil {
			s.logger.Error("docker provisioning failed", "error", provErr.Error(), "family_id", req.FamilyID)
			containerStatus = "provision_failed"
		} else {
			dockerPort = port
			// Record the container in family_containers table.
			r2Prefix := "flock-family-private/" + req.FamilyID + "/"
			_, _ = s.db.ExecContext(r.Context(), `
				INSERT INTO family_containers (family_id, docker_host, postgres_port, status, r2_prefix, provisioned_at)
				VALUES ($1, $2, $3, 'active', $4, NOW())
				ON CONFLICT (family_id) DO UPDATE
				  SET status = 'active',
				      postgres_port = EXCLUDED.postgres_port,
				      provisioned_at = COALESCE(family_containers.provisioned_at, NOW())`,
				req.FamilyID, getEnv("DOCKER_HOST_INTERNAL", "127.0.0.1"), dockerPort, r2Prefix,
			)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"roost_family_id":      req.FamilyID,
		"plan":                 plan,
		"roost_token_endpoint": "https://roost.yourflock.org/auth/flock",
		"docker_provisioned":   containerStatus == "provisioned",
		"docker_port":          dockerPort,
		"status":               containerStatus,
	})
}

// handleSSOrevoke deactivates a family's Flock TV access.
// DELETE /internal/flocktv/revoke/{family_id}
// Called by Flock backend when a subscriber cancels or loses their TV add-on.
func (s *Server) handleSSOrevoke(w http.ResponseWriter, r *http.Request) {
	if !checkInternalSecret(w, r) {
		return
	}

	familyID := chi.URLParam(r, "family_id")
	if familyID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "family_id is required")
		return
	}

	if s.db != nil {
		// Deactivate subscriber — don't delete (data retained for 30 days).
		_, _ = s.db.ExecContext(r.Context(), `
			UPDATE subscribers
			SET flocktv_tier = NULL, roost_boost_active = false
			WHERE id = $1`,
			familyID,
		)

		// Suspend (not delete) family container.
		_, _ = s.db.ExecContext(r.Context(), `
			UPDATE family_containers
			SET status = 'suspended', updated_at = NOW()
			WHERE family_id = $1`,
			familyID,
		)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "revoked",
		"family_id": familyID,
		"note":      "family data retained for 30 days; container suspended",
	})
}
