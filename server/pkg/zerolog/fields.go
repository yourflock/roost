// fields.go — Permitted log field allowlist for the Roost zero-logging policy.
//
// These are the ONLY fields that may appear in stream-endpoint log lines.
// Any other field is silently dropped by SafeLogger.sanitize().
//
// Guiding principle: a log line must not contain anything that could identify
// a specific subscriber or reveal what they watched, when, or from where.
//
// Fields explicitly banned (examples — not exhaustive):
//   - ip, remote_ip, client_ip, x_forwarded_for: subscriber network identity
//   - subscriber_id, user_id, account_id: subscriber identity
//   - token, api_token, session_token, auth_token: credential material
//   - user_agent: browser/device fingerprint
//   - query_string: may contain token param or other PII
//   - referer, referrer: page context that could identify subscriber
//   - stream_id, channel_id: content being watched (privacy-sensitive)
//   - family_id: family identity
package zerolog

// PermittedFields is the definitive allowlist.
// Add new fields ONLY after privacy review — document the reason here.
var PermittedFields = map[string]struct{}{
	// Request metadata — safe because these describe the HTTP exchange
	// without identifying who made it.
	"request_id": {}, // UUID generated per-request (not subscriber-linked)
	"status":     {}, // HTTP response code
	"method":     {}, // HTTP method (GET, HEAD)

	// Path prefix only — e.g. "/hls/" or "/relay/". Never the full path,
	// which could contain stream IDs linked to subscriber accounts.
	"path_prefix": {},

	// Timing — useful for performance analysis, not identifying.
	"duration_ms": {},

	// Cache status — HIT or MISS.
	"cache": {},

	// Content type — "video/MP2T", "application/vnd.apple.mpegurl", etc.
	// Describes what was served, not who received it.
	"content_type": {},

	// Cloudflare Ray ID — opaque identifier for a CF request. Useful for
	// opening support tickets with Cloudflare. Not subscriber-linked in
	// Roost's logging (CF may have its own linkage, but that's CF-internal).
	"cf_ray": {},

	// Error description — must be a technical error string, never including
	// subscriber data. E.g. "upstream timeout after 5s", not "token rejected
	// for subscriber X".
	"error": {},

	// Service name (set once per logger instance, always safe).
	"service": {},

	// Message field used by LogWarn.
	"message": {},

	// Health check specific.
	"event": {},
}

// isPermitted returns true if the field name is in the allowlist.
func isPermitted(field string) bool {
	_, ok := PermittedFields[field]
	return ok
}
