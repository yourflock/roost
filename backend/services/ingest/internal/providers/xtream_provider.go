// xtream_provider.go — Xtream Codes API ingest provider.
//
// Connects to an upstream Xtream Codes-compatible IPTV provider using
// host + username + password credentials. Fetches live streams, VOD, and EPG.
//
// SECURITY: Credentials are never written to any log line. All log output
// uses safeLog() which redacts username/password from URLs.
//
// Config keys:
//   host     — base URL of the Xtream provider (e.g. http://provider.example.com:8080)
//   username — Xtream account username
//   password — Xtream account password
//
// Stream URL format (HLS):
//   {host}/live/{username}/{password}/{stream_id}.m3u8
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// xtreamProvider implements IngestProvider for Xtream Codes sources.
type xtreamProvider struct {
	host     string
	username string
	password string
	client   *http.Client
}

// newXtreamProvider validates config and returns an xtreamProvider.
func newXtreamProvider(config map[string]string) (*xtreamProvider, error) {
	host := strings.TrimRight(config["host"], "/")
	username := config["username"]
	password := config["password"]

	if host == "" {
		return nil, fmt.Errorf("xtream provider requires config key 'host'")
	}
	if username == "" {
		return nil, fmt.Errorf("xtream provider requires config key 'username'")
	}
	if password == "" {
		return nil, fmt.Errorf("xtream provider requires config key 'password'")
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		return nil, fmt.Errorf("xtream host must start with http:// or https://")
	}

	return &xtreamProvider{
		host:     host,
		username: username,
		password: password,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (p *xtreamProvider) Type() string { return "xtream" }

func (p *xtreamProvider) Validate(config map[string]string) error {
	_, err := newXtreamProvider(config)
	return err
}

// GetChannels fetches all live streams from the Xtream API.
func (p *xtreamProvider) GetChannels(ctx context.Context) ([]IngestChannel, error) {
	streams, err := p.GetLiveStreams(ctx)
	if err != nil {
		return nil, err
	}
	cats, err := p.GetLiveCategories(ctx)
	if err != nil {
		// Categories are non-critical — proceed without them.
		cats = nil
	}
	catMap := make(map[string]string, len(cats))
	for _, c := range cats {
		catMap[c.CategoryID] = c.CategoryName
	}

	channels := make([]IngestChannel, 0, len(streams))
	for _, s := range streams {
		ch := IngestChannel{
			ID:        s.StreamID,
			Name:      s.Name,
			LogoURL:   s.StreamIcon,
			TvgID:     s.EpgChannelID,
			StreamURL: p.buildStreamURL(s.StreamID),
		}
		if catName, ok := catMap[s.CategoryID]; ok {
			ch.Category = catName
		} else {
			ch.Category = s.CategoryID
		}
		if ch.Name == "" {
			ch.Name = s.StreamID
		}
		channels = append(channels, ch)
	}
	return channels, nil
}

// GetStreamURL constructs the HLS stream URL for a given stream ID.
// The returned URL contains credentials and must never be logged or returned to clients.
func (p *xtreamProvider) GetStreamURL(_ context.Context, streamID string) (string, error) {
	if streamID == "" {
		return "", fmt.Errorf("xtream: empty streamID")
	}
	return p.buildStreamURL(streamID), nil
}

// HealthCheck calls the server info endpoint to verify connectivity and account status.
func (p *xtreamProvider) HealthCheck(ctx context.Context) error {
	info, err := p.GetServerInfo(ctx)
	if err != nil {
		return err
	}
	if info.UserInfo.Status == "Expired" {
		return fmt.Errorf("xtream account expired")
	}
	return nil
}

// ---------- Xtream API methods -----------------------------------------------

// XtreamServerInfo is the response from the server info endpoint.
type XtreamServerInfo struct {
	UserInfo   XtreamUserInfo   `json:"user_info"`
	ServerInfo XtreamServerMeta `json:"server_info"`
}

// XtreamUserInfo contains account details.
type XtreamUserInfo struct {
	Username    string `json:"username"`
	Status      string `json:"status"`       // "Active", "Expired", "Banned"
	ExpDate     string `json:"exp_date"`     // unix timestamp or ""
	MaxConnections string `json:"max_connections"`
	ActiveConnections string `json:"active_cons"`
}

// XtreamServerMeta contains server metadata.
type XtreamServerMeta struct {
	URL        string `json:"url"`
	Port       string `json:"port"`
	HTTPSPort  string `json:"https_port"`
	ServerProtocol string `json:"server_protocol"`
	RTMPPort   string `json:"rtmp_port"`
	Timezone   string `json:"timezone"`
}

// XtreamCategory represents a stream category.
type XtreamCategory struct {
	CategoryID   string `json:"category_id"`
	CategoryName string `json:"category_name"`
	ParentID     int    `json:"parent_id"`
}

// XtreamLiveStream represents a single live stream entry.
type XtreamLiveStream struct {
	Num          int    `json:"num"`
	Name         string `json:"name"`
	StreamType   string `json:"stream_type"`
	StreamID     string `json:"stream_id"`
	StreamIcon   string `json:"stream_icon"`
	EpgChannelID string `json:"epg_channel_id"`
	Added        string `json:"added"`
	CategoryID   string `json:"category_id"`
	CustomSID    string `json:"custom_sid"`
	TvArchive    int    `json:"tv_archive"`
	DirectSource string `json:"direct_source"`
}

// GetServerInfo calls the server info endpoint (empty action).
func (p *xtreamProvider) GetServerInfo(ctx context.Context) (*XtreamServerInfo, error) {
	var result XtreamServerInfo
	if err := p.apiCall(ctx, "", &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetLiveCategories fetches all live stream categories.
func (p *xtreamProvider) GetLiveCategories(ctx context.Context) ([]XtreamCategory, error) {
	var result []XtreamCategory
	if err := p.apiCall(ctx, "get_live_categories", &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetLiveStreams fetches all live streams.
func (p *xtreamProvider) GetLiveStreams(ctx context.Context) ([]XtreamLiveStream, error) {
	var result []XtreamLiveStream
	if err := p.apiCall(ctx, "get_live_streams", &result); err != nil {
		return nil, err
	}
	return result, nil
}

// XtreamEPGEntry is one EPG program from the Xtream API.
type XtreamEPGEntry struct {
	ID          string `json:"id"`
	EpgID       string `json:"epg_id"`
	Title       string `json:"title"`
	Lang        string `json:"lang"`
	Start       string `json:"start"`
	End         string `json:"end"`
	Description string `json:"description"`
	ChannelID   string `json:"channel_id"`
	StartTimestamp string `json:"start_timestamp"`
	StopTimestamp  string `json:"stop_timestamp"`
}

// XtreamEPGResponse wraps the EPG API response.
type XtreamEPGResponse struct {
	EPGListings []XtreamEPGEntry `json:"epg_listings"`
}

// GetShortEPG fetches EPG data for a stream (up to limit hours).
func (p *xtreamProvider) GetShortEPG(ctx context.Context, streamID string, limit int) (*XtreamEPGResponse, error) {
	apiURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s&action=get_short_epg&stream_id=%s&limit=%d",
		p.host,
		url.QueryEscape(p.username),
		url.QueryEscape(p.password),
		url.QueryEscape(streamID),
		limit,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("xtream epg request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xtream epg fetch: %w", err)
	}
	defer resp.Body.Close()

	var result XtreamEPGResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("xtream epg decode: %w", err)
	}
	return &result, nil
}

// ---------- internal ---------------------------------------------------------

// apiCall makes a player_api.php call with the given action and decodes JSON into dest.
// Credentials appear in the URL but never in log output.
func (p *xtreamProvider) apiCall(ctx context.Context, action string, dest interface{}) error {
	apiURL := fmt.Sprintf("%s/player_api.php?username=%s&password=%s",
		p.host,
		url.QueryEscape(p.username),
		url.QueryEscape(p.password),
	)
	if action != "" {
		apiURL += "&action=" + url.QueryEscape(action)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return fmt.Errorf("xtream api request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("xtream api call (action=%q host=%s): %w",
			action, safeXtreamHost(p.host), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("xtream api: HTTP %d for action=%q host=%s",
			resp.StatusCode, action, safeXtreamHost(p.host))
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("xtream api decode (action=%q): %w", action, err)
	}
	return nil
}

// buildStreamURL assembles the HLS stream URL for a stream ID.
// The URL contains plaintext credentials — never log or return to clients.
func (p *xtreamProvider) buildStreamURL(streamID string) string {
	return fmt.Sprintf("%s/live/%s/%s/%s.m3u8", p.host, p.username, p.password, streamID)
}

// safeXtreamHost returns only the host portion of the Xtream URL for log output.
func safeXtreamHost(host string) string {
	u, err := url.Parse(host)
	if err != nil {
		return "[unparseable]"
	}
	return u.Host
}
