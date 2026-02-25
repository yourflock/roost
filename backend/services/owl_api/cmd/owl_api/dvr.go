// dvr.go — DVR endpoints for the Owl addon API.
// Proxies DVR requests to the internal DVR service.
// All routes require Owl session token (enforced by requireSession middleware).
//
// Routes:
//   GET    /owl/dvr          — list subscriber's recordings + quota
//   POST   /owl/dvr          — schedule new recording
//   DELETE /owl/dvr/:id      — delete recording
//   GET    /owl/dvr/quota    — quota info only
//   GET    /owl/dvr/:id/play — proxy DVR recording HLS playlist
//   GET    /owl/v1/dvr       — v1 aliases for all of the above
package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// dvrServiceURL returns the base URL of the internal DVR service.
func dvrServiceURL() string {
	if v := os.Getenv("DVR_SERVICE_URL"); v != "" {
		return v
	}
	return "http://localhost:8101"
}

// dvrSubscriberID extracts the subscriber ID from the request.
// requireSession middleware sets X-Subscriber-ID before calling handlers.
func dvrSubscriberID(r *http.Request) string {
	return r.Header.Get("X-Subscriber-ID")
}

// handleDVR handles GET /owl/dvr and POST /owl/dvr.
// GET: returns subscriber's recording list + quota.
// POST: schedules a new recording.
func (s *server) handleDVR(w http.ResponseWriter, r *http.Request) {
	subID := dvrSubscriberID(r)
	if subID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "subscriber session required")
		return
	}

	baseURL := dvrServiceURL()
	var upstreamURL string
	var body io.Reader

	switch r.Method {
	case http.MethodGet:
		upstreamURL = baseURL + "/dvr/recordings"
		body = nil
	case http.MethodPost:
		upstreamURL = baseURL + "/dvr/recordings"
		body = r.Body
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_error", "failed to build upstream request")
		return
	}
	req.Header.Set("X-Subscriber-ID", subID)
	if r.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}

	proxyResponse(w, req)
}

// handleDVRItem handles DELETE /owl/dvr/:id.
func (s *server) handleDVRItem(w http.ResponseWriter, r *http.Request) {
	subID := dvrSubscriberID(r)
	if subID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "subscriber session required")
		return
	}

	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
		return
	}

	// Strip /owl/dvr/ or /owl/v1/dvr/ prefix to get the ID
	path := r.URL.Path
	for _, prefix := range []string{"/owl/v1/dvr/", "/owl/dvr/"} {
		if strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
			break
		}
	}
	id := strings.Split(strings.Trim(path, "/"), "/")[0]
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "recording id required")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodDelete,
		dvrServiceURL()+"/dvr/recordings/"+id, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_error", "")
		return
	}
	req.Header.Set("X-Subscriber-ID", subID)
	proxyResponse(w, req)
}

// handleDVRQuota handles GET /owl/dvr/quota.
func (s *server) handleDVRQuota(w http.ResponseWriter, r *http.Request) {
	subID := dvrSubscriberID(r)
	if subID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "subscriber session required")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		dvrServiceURL()+"/dvr/quota", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_error", "")
		return
	}
	req.Header.Set("X-Subscriber-ID", subID)
	proxyResponse(w, req)
}

// handleDVRPlay handles GET /owl/dvr/:id/play — proxy HLS playlist for playback.
func (s *server) handleDVRPlay(w http.ResponseWriter, r *http.Request) {
	subID := dvrSubscriberID(r)
	if subID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "subscriber session required")
		return
	}

	// Extract :id from /owl/dvr/:id/play or /owl/v1/dvr/:id/play
	path := r.URL.Path
	for _, prefix := range []string{"/owl/v1/dvr/", "/owl/dvr/"} {
		path = strings.TrimPrefix(path, prefix)
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "recording id required")
		return
	}
	id := parts[0]

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		dvrServiceURL()+"/dvr/recordings/"+id+"/play", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_error", "")
		return
	}
	req.Header.Set("X-Subscriber-ID", subID)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "dvr_unavailable", "DVR service unavailable")
		return
	}
	defer resp.Body.Close()

	// Pass through content type (m3u8 or error JSON)
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/vnd.apple.mpegurl"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// proxyResponse executes an upstream HTTP request and writes the response to w.
func proxyResponse(w http.ResponseWriter, req *http.Request) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "dvr_unavailable", "DVR service unavailable")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_error", "failed to read response")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
