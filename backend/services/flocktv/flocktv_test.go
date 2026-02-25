// flocktv_test.go — Unit tests for the Flock TV service.
// Tests cover stream gateway, SSO provision, Roost Boost contribution validation,
// and signed URL generation without any external dependencies.
package flocktv

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestServer returns a Server with a nil DB — suitable for handler unit tests
// that do not yet have real DB wiring.
func newTestServer() *Server {
	return NewServer(nil)
}

// ── Stream gateway ────────────────────────────────────────────────────────────

func TestStreamRequest_OK(t *testing.T) {
	os.Setenv("CDN_RELAY_URL", "https://stream.yourflock.org")
	os.Setenv("CDN_HMAC_SECRET", "test-secret-32-bytes-long-padded!!")

	body := `{"family_id":"fam-123","canonical_id":"imdb:tt0111161","quality":"1080p"}`
	req := httptest.NewRequest(http.MethodPost, "/flocktv/stream", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestServer().handleStreamRequest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp StreamResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.SignedURL == "" {
		t.Error("expected SignedURL to be non-empty")
	}
	if resp.ExpiresAt == 0 {
		t.Error("expected ExpiresAt to be set")
	}
	if resp.ExpiresAt < time.Now().Unix() {
		t.Error("ExpiresAt must be in the future")
	}
}

func TestStreamRequest_MissingFields(t *testing.T) {
	os.Setenv("CDN_HMAC_SECRET", "test-secret-32-bytes-long-padded!!")

	tests := []struct {
		name string
		body string
	}{
		{"empty body", `{}`},
		{"missing canonical_id", `{"family_id":"fam-123"}`},
		{"missing family_id", `{"canonical_id":"imdb:tt0111161"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/flocktv/stream", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			newTestServer().handleStreamRequest(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestStreamRequest_MissingHMACSecret(t *testing.T) {
	os.Unsetenv("CDN_HMAC_SECRET")

	body := `{"family_id":"fam-123","canonical_id":"imdb:tt0111161","quality":"1080p"}`
	req := httptest.NewRequest(http.MethodPost, "/flocktv/stream", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestServer().handleStreamRequest(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when CDN_HMAC_SECRET unset, got %d", w.Code)
	}
}

func TestStreamStart_OK(t *testing.T) {
	body := `{"family_id":"fam-123","canonical_id":"imdb:tt0111161","quality":"1080p"}`
	req := httptest.NewRequest(http.MethodPost, "/flocktv/stream/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestServer().handleStreamStart(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStreamEnd_MissingEventID(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/flocktv/stream/end", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestServer().handleStreamEnd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── Signed URL generation ─────────────────────────────────────────────────────

func TestGenerateSignedURL_NonEmpty(t *testing.T) {
	url := generateSignedURL("https://stream.yourflock.org", "test-secret",
		"/content/imdb:tt0111161/manifest.m3u8", 15*time.Minute)
	if url == "" {
		t.Error("expected non-empty signed URL")
	}
	if !strings.Contains(url, "expires=") {
		t.Error("signed URL must contain expires parameter")
	}
	if !strings.Contains(url, "sig=") {
		t.Error("signed URL must contain sig parameter")
	}
}

func TestGenerateSignedURL_DifferentSecrets(t *testing.T) {
	url1 := generateSignedURL("https://stream.yourflock.org", "secret-A",
		"/content/imdb:tt0111161/manifest.m3u8", 15*time.Minute)
	url2 := generateSignedURL("https://stream.yourflock.org", "secret-B",
		"/content/imdb:tt0111161/manifest.m3u8", 15*time.Minute)
	if url1 == url2 {
		t.Error("different HMAC secrets must produce different signatures")
	}
}

func TestGenerateSignedURL_DifferentPaths(t *testing.T) {
	url1 := generateSignedURL("https://stream.yourflock.org", "secret",
		"/content/imdb:tt0111161/manifest.m3u8", 15*time.Minute)
	url2 := generateSignedURL("https://stream.yourflock.org", "secret",
		"/content/imdb:tt0000001/manifest.m3u8", 15*time.Minute)
	if url1 == url2 {
		t.Error("different paths must produce different signatures")
	}
}

// ── SSO provision ─────────────────────────────────────────────────────────────

func TestSSOProvision_Unauthorized_NoHeader(t *testing.T) {
	os.Setenv("FLOCK_INTERNAL_SECRET", "secret-token-abc123")

	body := `{"family_id":"fam-123","user_id":"usr-456","plan":"flock_family_tv"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/flocktv/provision", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestServer().handleSSOprovision(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestSSOProvision_Unauthorized_WrongSecret(t *testing.T) {
	os.Setenv("FLOCK_INTERNAL_SECRET", "secret-token-abc123")

	body := `{"family_id":"fam-123","user_id":"usr-456","plan":"flock_family_tv"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/flocktv/provision", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flock-Internal-Secret", "wrong-secret")
	w := httptest.NewRecorder()

	newTestServer().handleSSOprovision(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestSSOProvision_Authorized_OK(t *testing.T) {
	os.Setenv("FLOCK_INTERNAL_SECRET", "secret-token-abc123")

	body := `{"family_id":"fam-123","user_id":"usr-456","plan":"flock_family_tv","flock_jwt_public_key":"base64key=="}`
	req := httptest.NewRequest(http.MethodPost, "/internal/flocktv/provision", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flock-Internal-Secret", "secret-token-abc123")
	w := httptest.NewRecorder()

	newTestServer().handleSSOprovision(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["roost_family_id"] != "fam-123" {
		t.Errorf("unexpected roost_family_id: %v", resp["roost_family_id"])
	}
	if resp["status"] != "provisioned" {
		t.Errorf("unexpected status: %v", resp["status"])
	}
}

func TestSSOProvision_MissingRequiredFields(t *testing.T) {
	os.Setenv("FLOCK_INTERNAL_SECRET", "secret-token-abc123")

	body := `{"family_id":"fam-123"}` // missing user_id
	req := httptest.NewRequest(http.MethodPost, "/internal/flocktv/provision", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flock-Internal-Secret", "secret-token-abc123")
	w := httptest.NewRecorder()

	newTestServer().handleSSOprovision(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── Roost Boost contribution ──────────────────────────────────────────────────

func TestContribute_ValidM3U(t *testing.T) {
	os.Setenv("ROOST_CREDS_KEY", "test-key-32-bytes-for-aes256!!!")

	body := `{"source_type":"m3u_url","credentials":"http://provider.com/get.php?username=user&password=pass&type=m3u_plus","label":"My IPTV"}`
	req := httptest.NewRequest(http.MethodPost, "/flocktv/boost/contribute", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestServer().handleContribute(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestContribute_InvalidSourceType(t *testing.T) {
	body := `{"source_type":"unknown","credentials":"http://example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/flocktv/boost/contribute", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestServer().handleContribute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestContribute_MissingCredentials(t *testing.T) {
	body := `{"source_type":"m3u_url","credentials":""}`
	req := httptest.NewRequest(http.MethodPost, "/flocktv/boost/contribute", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestServer().handleContribute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── Credential encryption ─────────────────────────────────────────────────────

func TestEncryptCredentials_ProducesNonEmptyOutput(t *testing.T) {
	os.Setenv("ROOST_CREDS_KEY", "test-32-byte-key-for-unit-tests!!")

	ciphertext, nonce, err := encryptCredentials([]byte("http://provider.com/m3u"))
	if err != nil {
		t.Fatalf("encryption error: %v", err)
	}
	if len(ciphertext) == 0 {
		t.Error("expected non-empty ciphertext")
	}
	if len(nonce) == 0 {
		t.Error("expected non-empty nonce")
	}
}

func TestEncryptCredentials_DifferentNoncesEachCall(t *testing.T) {
	os.Setenv("ROOST_CREDS_KEY", "test-32-byte-key-for-unit-tests!!")

	plaintext := []byte("http://provider.com/m3u")
	_, nonce1, _ := encryptCredentials(plaintext)
	_, nonce2, _ := encryptCredentials(plaintext)

	if bytes.Equal(nonce1, nonce2) {
		t.Error("GCM nonces must be unique per call (crypto/rand)")
	}
}

func TestEncryptCredentials_MissingKey(t *testing.T) {
	os.Unsetenv("ROOST_CREDS_KEY")

	_, _, err := encryptCredentials([]byte("test"))
	if err == nil {
		t.Error("expected error when ROOST_CREDS_KEY is not set")
	}
}

// ── Billing calculation ───────────────────────────────────────────────────────

func TestCalculateCharge_BaseOnly(t *testing.T) {
	usage := FamilyUsage{
		FamilyID:    "fam-123",
		StreamHours: 0,
	}
	charged := CalculateCharge(usage)
	if charged.BaseCharge != 4.99 {
		t.Errorf("expected base charge 4.99, got %f", charged.BaseCharge)
	}
	if charged.UsageCharge != 0.00 {
		t.Errorf("expected usage charge 0.00, got %f", charged.UsageCharge)
	}
	if charged.TotalCharge != 4.99 {
		t.Errorf("expected total charge 4.99, got %f", charged.TotalCharge)
	}
}

func TestCalculateCharge_WithUsage(t *testing.T) {
	usage := FamilyUsage{
		FamilyID:    "fam-456",
		StreamHours: 10.5, // 10.5 hours * $0.01 = $0.11 (rounded)
	}
	charged := CalculateCharge(usage)
	if charged.UsageCharge != 0.11 {
		t.Errorf("expected usage charge 0.11, got %f", charged.UsageCharge)
	}
	if charged.TotalCharge != 5.10 {
		t.Errorf("expected total charge 5.10, got %f", charged.TotalCharge)
	}
}

// ── Selections validation ─────────────────────────────────────────────────────

func TestAddSelection_InvalidContentType(t *testing.T) {
	body := `{"canonical_id":"imdb:tt0111161","content_type":"invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/flocktv/selections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestServer().handleAddSelection(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAddSelection_MissingCanonicalID(t *testing.T) {
	body := `{"content_type":"movie"}`
	req := httptest.NewRequest(http.MethodPost, "/flocktv/selections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	newTestServer().handleAddSelection(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── Channel normalization ─────────────────────────────────────────────────────

func TestNormalizeChannelName_Basic(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"CNN International", "cnn international"},
		{"CNN-International (HD)", "cnn international hd"},
		{"BBC One HD", "bbc one"},
		{"  ESPN  ", "espn"},
		{"Fox News FHD", "fox news"},
		{"Al Jazeera English 4K", "al jazeera english"},
	}

	for _, tc := range tests {
		result := normalizeChannelName(tc.input)
		if result != tc.expected {
			t.Errorf("normalizeChannelName(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestNormalizeChannelName_Empty(t *testing.T) {
	result := normalizeChannelName("")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

// ── IPTV credential validation ────────────────────────────────────────────────

func TestValidateIPTVCredentials_ValidM3U(t *testing.T) {
	err := validateIPTVCredentials("m3u_url", "https://provider.com/get.php?username=u&password=p")
	if err != nil {
		t.Errorf("expected valid M3U URL to pass, got: %v", err)
	}
}

func TestValidateIPTVCredentials_InvalidM3U_HTTP(t *testing.T) {
	err := validateIPTVCredentials("m3u_url", "ftp://provider.com/list.m3u")
	if err == nil {
		t.Error("expected FTP URL to fail validation")
	}
}

func TestValidateIPTVCredentials_InvalidM3U_Spaces(t *testing.T) {
	err := validateIPTVCredentials("m3u_url", "http://provider.com/my list.m3u")
	if err == nil {
		t.Error("expected URL with spaces to fail validation")
	}
}

func TestValidateIPTVCredentials_ValidXtream(t *testing.T) {
	creds := `{"host":"http://provider.com","port":"8080","username":"u","password":"p"}`
	err := validateIPTVCredentials("xtream", creds)
	if err != nil {
		t.Errorf("expected valid Xtream credentials to pass, got: %v", err)
	}
}

func TestValidateIPTVCredentials_EmptyXtream(t *testing.T) {
	err := validateIPTVCredentials("xtream", "short")
	if err == nil {
		t.Error("expected short Xtream credentials to fail")
	}
}

// ── Billing period validation ─────────────────────────────────────────────────

func TestBillingUsage_BatchMaxFamilies(t *testing.T) {
	os.Setenv("FLOCK_INTERNAL_SECRET", "secret-token-abc123")

	// Build a batch with 501 families (over the 500 limit).
	type entry struct {
		FamilyID    string `json:"family_id"`
		PeriodStart string `json:"period_start"`
		PeriodEnd   string `json:"period_end"`
	}
	type batchBody struct {
		Families []entry `json:"families"`
	}

	families := make([]entry, 501)
	for i := range families {
		families[i] = entry{
			FamilyID:    "fam-" + strconv.Itoa(i),
			PeriodStart: "2026-01-01",
			PeriodEnd:   "2026-02-01",
		}
	}

	body, _ := json.Marshal(batchBody{Families: families})
	req := httptest.NewRequest(http.MethodPost, "/internal/billing/usage/batch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flock-Internal-Secret", "secret-token-abc123")
	w := httptest.NewRecorder()

	newTestServer().handleBillingUsageBatch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for over-limit batch, got %d", w.Code)
	}
}

func TestBillingUsage_SingleFamily_NoSecret(t *testing.T) {
	os.Setenv("FLOCK_INTERNAL_SECRET", "secret-token-abc123")

	req := httptest.NewRequest(http.MethodGet,
		"/internal/billing/usage?family_id=fam-123&period_start=2026-01-01&period_end=2026-02-01",
		nil)
	w := httptest.NewRecorder()

	newTestServer().handleBillingUsage(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without secret, got %d", w.Code)
	}
}

func TestBillingUsage_PeriodTooLong(t *testing.T) {
	os.Setenv("FLOCK_INTERNAL_SECRET", "secret-token-abc123")

	req := httptest.NewRequest(http.MethodGet,
		"/internal/billing/usage?family_id=fam-123&period_start=2026-01-01&period_end=2026-03-15",
		nil)
	req.Header.Set("X-Flock-Internal-Secret", "secret-token-abc123")
	w := httptest.NewRecorder()

	newTestServer().handleBillingUsage(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for period > 35 days, got %d", w.Code)
	}
}

// ── Boost channels requires active boost ─────────────────────────────────────

func TestBoostChannels_RequiresBoost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/flocktv/boost/channels", nil)
	w := httptest.NewRecorder()

	newTestServer().handleBoostChannels(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without boost, got %d", w.Code)
	}
}

func TestBoostChannels_DevModeHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/flocktv/boost/channels", nil)
	req.Header.Set("X-Roost-Boost", "true")
	w := httptest.NewRecorder()

	newTestServer().handleBoostChannels(w, req)

	// Without DB, returns 200 with empty channels.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with dev boost header, got %d: %s", w.Code, w.Body.String())
	}
}

// ── Acquisition status ────────────────────────────────────────────────────────

func TestAcquisitionStatus_NoDB(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/flocktv/acquire/imdb:tt0111161", nil)
	req = withChiParam(req, "canonical_id", "imdb:tt0111161")
	w := httptest.NewRecorder()

	newTestServer().handleAcquisitionStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
