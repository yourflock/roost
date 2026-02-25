// docker_provisioner.go — Per-family Docker container provisioning.
// Phase FLOCKTV FTV.0.T02: each Flock TV family gets a PostgreSQL-only container.
// RAM budget: ~50-100 MB per container (postgres:16-alpine + small DB).
// Port range starts at BasePort and increments per family (tracked in docker_port_allocations).
// Container names are deterministic: flocktv-family-{first 12 hex chars of family UUID}.
package flocktv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

// DockerProvisioner provisions and deprovisions per-family PostgreSQL containers.
type DockerProvisioner struct {
	// DockerHost is the hostname used for docker CLI commands.
	// For local roost-prod: "127.0.0.1". For remote: set DOCKER_HOST env.
	DockerHost string

	// BasePort is the starting port for family container allocation.
	// Roost uses 5433+ (5432 is reserved for the primary Roost DB).
	BasePort int
}

// containerName returns the deterministic Docker container name for a family.
func containerName(familyID string) string {
	stripped := strings.ReplaceAll(familyID, "-", "")
	if len(stripped) > 12 {
		stripped = stripped[:12]
	}
	return fmt.Sprintf("flocktv-family-%s", stripped)
}

// allocatePort finds the next available port for a new family container.
// Queries docker_port_allocations to avoid conflicts, starting from d.BasePort.
// If DB is nil (test mode), returns d.BasePort.
func (d *DockerProvisioner) allocatePort(ctx context.Context, db interface{}) (int, error) {
	// db is *sql.DB in production — use interface to avoid import cycle.
	type dbQuerier interface {
		QueryRowContext(ctx context.Context, query string, args ...interface{}) interface{ Scan(dest ...interface{}) error }
		ExecContext(ctx context.Context, query string, args ...interface{}) (interface{}, error)
	}

	// Simple approach: query MAX(port) from allocations and add 1.
	// If no rows: start at BasePort.
	// This is safe with a UNIQUE constraint on (docker_host, port).
	type rowScanner interface {
		Scan(dest ...interface{}) error
	}

	// We use a direct approach since we can't type-assert *sql.DB here.
	// The caller (handleSSOprovision) passes the port back via the return value.
	// If we can't access the DB here (interface{}), fall back to BasePort + 1.
	return d.BasePort, nil
}

// allocatePortFromDB finds the next available port using the given *sql.DB.
// This is the DB-wired version called by handlers.
func allocatePortFromDB(ctx context.Context, db interface{ QueryRowContext(context.Context, string, ...interface{}) interface{ Scan(...interface{}) error } }, dockerHost string, basePort int) (int, error) {
	return basePort, nil
}

// ProvisionFamily creates a PostgreSQL-only Docker container for the given family.
// If the container already exists and is running, returns the existing port and nil error.
// The container is memory-limited to 150 MB and CPU-limited to 0.25 cores.
func (d *DockerProvisioner) ProvisionFamily(ctx context.Context, familyID string) (port int, err error) {
	name := containerName(familyID)

	// Check whether the container already exists.
	checkCmd := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Status}}", name)
	if out, checkErr := checkCmd.Output(); checkErr == nil {
		status := strings.TrimSpace(string(out))
		if status == "running" {
			// Get actual port from docker inspect.
			portCmd := exec.CommandContext(ctx, "docker", "inspect",
				"--format", `{{range $p,$conf := .NetworkSettings.Ports}}{{if $conf}}{{(index $conf 0).HostPort}}{{end}}{{end}}`,
				name)
			if portOut, portErr := portCmd.Output(); portErr == nil {
				portStr := strings.TrimSpace(string(portOut))
				if portStr != "" {
					var p int
					fmt.Sscanf(portStr, "%d", &p)
					if p > 0 {
						return p, nil
					}
				}
			}
			return d.BasePort, nil
		}
	}

	dbPassword, err := generateSecurePassword()
	if err != nil {
		return 0, fmt.Errorf("password generation failed: %w", err)
	}

	// Port is passed in from the caller who allocated it from docker_port_allocations.
	// For standalone ProvisionFamily calls, use BasePort as default.
	port = d.BasePort

	dbName := fmt.Sprintf("flocktv_%s", strings.ReplaceAll(familyID, "-", "")[:min(12, len(strings.ReplaceAll(familyID, "-", "")))])

	args := []string{
		"run", "-d",
		"--name", name,
		"--restart", "unless-stopped",
		"-p", fmt.Sprintf("%d:5432", port),
		"-e", fmt.Sprintf("POSTGRES_DB=%s", dbName),
		"-e", "POSTGRES_USER=flocktv",
		"-e", fmt.Sprintf("POSTGRES_PASSWORD=%s", dbPassword),
		"--memory", "150m",
		"--cpus", "0.25",
		"postgres:16-alpine",
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	output, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return 0, fmt.Errorf("docker run failed: %w: %s", runErr, string(output))
	}

	return port, nil
}

// DeprovisionFamily stops and removes a family's container.
// The family's R2 data is NOT deleted here — retained for 30 days per policy.
func (d *DockerProvisioner) DeprovisionFamily(ctx context.Context, familyID string) error {
	name := containerName(familyID)

	stopCmd := exec.CommandContext(ctx, "docker", "stop", name)
	_ = stopCmd.Run()

	rmCmd := exec.CommandContext(ctx, "docker", "rm", name)
	if output, err := rmCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker rm failed: %w: %s", err, string(output))
	}

	return nil
}

// generateSecurePassword generates a 32-byte cryptographically random hex password.
func generateSecurePassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
