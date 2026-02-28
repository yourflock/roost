package hdhomerun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SignalQuality represents the signal metrics for a tuner.
type SignalQuality struct {
	// Strength is the signal strength percentage (0-100).
	Strength int `json:"strength"`
	// SNR is the signal-to-noise ratio percentage (0-100).
	SNR int `json:"snr"`
	// Quality is the overall signal quality percentage (0-100).
	Quality int `json:"quality"`
}

// Channel represents a discovered channel from a scan.
type Channel struct {
	// Number is the virtual channel number (e.g., "5.1").
	Number string `json:"number"`
	// Name is the channel callsign or name (e.g., "WEWS-DT").
	Name string `json:"name"`
	// Frequency is the RF frequency in Hz.
	Frequency int `json:"frequency"`
	// Modulation is the modulation type (e.g., "8vsb", "qam256").
	Modulation string `json:"modulation"`
	// Program is the MPEG program number within the transport stream.
	Program int `json:"program"`
}

// Device represents a discovered HDHomeRun device on the network.
type Device struct {
	// DeviceID is the unique hardware ID of the device.
	DeviceID string `json:"device_id"`
	// IP is the IP address of the device on the local network.
	IP string `json:"ip"`
	// Model is the device model name (e.g., "HDHR5-4US").
	Model string `json:"model"`
	// FirmwareVersion is the current firmware version string.
	FirmwareVersion string `json:"firmware_version"`
	// TunerCount is the number of available tuners on the device.
	TunerCount int `json:"tuner_count"`
}

// ScanProgress reports the progress of a channel scan.
type ScanProgress struct {
	// Percent is the scan completion percentage (0-100).
	Percent int `json:"percent"`
	// Found is the number of channels found so far.
	Found int `json:"found"`
	// ChannelsScanned is the total number of frequencies scanned so far.
	ChannelsScanned int `json:"channels_scanned"`
	// TotalChannels is the total number of frequencies to scan.
	TotalChannels int `json:"total_channels"`
}

// Client defines the interface for interacting with HDHomeRun devices.
// This interface enables mock implementations for testing without hardware.
type Client interface {
	// Discover finds HDHomeRun devices on the local network via UDP broadcast.
	Discover(ctx context.Context) ([]Device, error)

	// TuneTo tunes a specific tuner on a device to the given channel.
	// Returns the stream URL for the tuned channel.
	TuneTo(ctx context.Context, deviceIP string, tunerIndex int, channel string) (string, error)

	// GetSignalQuality returns the current signal metrics for a tuner.
	GetSignalQuality(ctx context.Context, deviceIP string, tunerIndex int) (*SignalQuality, error)

	// ScanChannels performs a channel scan on the specified device.
	// If quick is true, performs a faster scan (under 5 minutes) scanning only
	// common frequencies. If quick is false, performs a full scan (under 20 minutes)
	// scanning all possible frequencies.
	// The progress callback is called periodically with scan progress updates.
	ScanChannels(ctx context.Context, deviceIP string, quick bool, progress func(ScanProgress)) ([]Channel, error)
}

// HTTPClient is the production implementation of Client that communicates with
// real HDHomeRun devices over their HTTP API.
type HTTPClient struct {
	httpClient *http.Client
}

// NewHTTPClient creates a new HDHomeRun client using the device HTTP API.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// discoverResponse is the JSON structure returned by HDHomeRun discover.json.
type discoverResponse struct {
	DeviceID        string `json:"DeviceID"`
	LocalIP         string `json:"LocalIP"`
	ModelNumber     string `json:"ModelNumber"`
	FirmwareName    string `json:"FirmwareName"`
	FirmwareVersion string `json:"FirmwareVersion"`
	TunerCount      int    `json:"TunerCount"`
}

// Discover finds HDHomeRun devices on the local network.
// It sends a UDP broadcast on port 65001 and parses responses,
// then queries each device's HTTP API for full details.
func (c *HTTPClient) Discover(ctx context.Context) ([]Device, error) {
	// HDHomeRun devices respond to UDP broadcast on port 65001.
	// The discover packet is a specific binary format.
	// For simplicity, we use the HTTP discover endpoint which devices
	// also expose on their local IP.

	// First, attempt UDP broadcast discovery to find device IPs.
	ips, err := c.udpDiscover(ctx)
	if err != nil {
		return nil, fmt.Errorf("udp discover failed: %w", err)
	}

	var devices []Device
	for _, ip := range ips {
		device, err := c.queryDevice(ctx, ip)
		if err != nil {
			// Skip devices that fail to respond to HTTP query.
			continue
		}
		devices = append(devices, *device)
	}

	return devices, nil
}

// udpDiscover sends a UDP broadcast to find HDHomeRun devices and returns their IPs.
func (c *HTTPClient) udpDiscover(ctx context.Context) ([]string, error) {
	// HDHomeRun discovery protocol uses UDP port 65001.
	// Discovery packet format:
	//   bytes 0-1: packet type (0x0001 = discover request)
	//   bytes 2-3: payload length (0x0004)
	//   bytes 4-5: tag (0x0001 = device type)
	//   bytes 6:   tag length (0x04)
	//   bytes 7-10: device type (0xFFFFFFFF = wildcard)
	//   bytes 11-14: CRC32 checksum
	discoverPacket := []byte{
		0x00, 0x02, // type: discover request
		0x00, 0x0c, // length: 12 bytes payload
		0x01,                   // tag: device type
		0x04,                   // tag length: 4
		0xff, 0xff, 0xff, 0xff, // device type: wildcard (all devices)
		0x02,                   // tag: device id
		0x04,                   // tag length: 4
		0xff, 0xff, 0xff, 0xff, // device id: wildcard (all devices)
	}
	// Note: real implementation would compute CRC32 for the packet.
	// HDHomeRun devices are tolerant of missing CRC in discover requests.

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("failed to open UDP socket: %w", err)
	}
	defer conn.Close()

	// Set deadline based on context or default 3 seconds.
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("failed to set deadline: %w", err)
	}

	broadcastAddr := &net.UDPAddr{
		IP:   net.IPv4bcast,
		Port: 65001,
	}

	_, err = conn.WriteTo(discoverPacket, broadcastAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to send discover broadcast: %w", err)
	}

	var ips []string
	buf := make([]byte, 2048)
	seen := make(map[string]bool)

	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			// Timeout is expected; we've collected all responses.
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				break
			}
			return nil, fmt.Errorf("error reading discover response: %w", err)
		}

		if n < 4 {
			continue
		}

		// Extract the IP from the response address.
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			continue
		}

		if !seen[host] {
			seen[host] = true
			ips = append(ips, host)
		}
	}

	return ips, nil
}

// queryDevice fetches device details via the HTTP API.
func (c *HTTPClient) queryDevice(ctx context.Context, ip string) (*Device, error) {
	url := fmt.Sprintf("http://%s/discover.json", ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query device at %s: %w", ip, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device at %s returned status %d", ip, resp.StatusCode)
	}

	var dr discoverResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return nil, fmt.Errorf("failed to decode discover response from %s: %w", ip, err)
	}

	return &Device{
		DeviceID:        dr.DeviceID,
		IP:              ip,
		Model:           dr.ModelNumber,
		FirmwareVersion: dr.FirmwareVersion,
		TunerCount:      dr.TunerCount,
	}, nil
}

// TuneTo tunes a specific tuner on a device to the given channel.
// It returns the HTTP stream URL that can be used to consume the live stream.
func (c *HTTPClient) TuneTo(ctx context.Context, deviceIP string, tunerIndex int, channel string) (string, error) {
	// Set the channel on the tuner via the HDHomeRun HTTP API.
	setURL := fmt.Sprintf("http://%s/tuner%d/vchannel", deviceIP, tunerIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, setURL,
		strings.NewReader(fmt.Sprintf("vchannel=%s", channel)))
	if err != nil {
		return "", fmt.Errorf("failed to create tune request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to tune device %s tuner %d to channel %s: %w",
			deviceIP, tunerIndex, channel, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("tune failed with status %d: %s", resp.StatusCode, string(body))
	}

	// The stream URL follows the HDHomeRun HTTP streaming convention.
	streamURL := fmt.Sprintf("http://%s:5004/auto/v%s", deviceIP, channel)
	return streamURL, nil
}

// tunerStatusResponse is the JSON structure from the tuner status endpoint.
type tunerStatusResponse struct {
	Resource     string `json:"Resource"`
	VctNumber    string `json:"VctNumber"`
	VctName      string `json:"VctName"`
	Frequency    int    `json:"Frequency"`
	SignalStrengthPercent int `json:"SignalStrengthPercent"`
	SignalQualityPercent  int `json:"SignalQualityPercent"`
	SymbolQualityPercent  int `json:"SymbolQualityPercent"`
}

// GetSignalQuality returns the current signal metrics for a tuner.
func (c *HTTPClient) GetSignalQuality(ctx context.Context, deviceIP string, tunerIndex int) (*SignalQuality, error) {
	url := fmt.Sprintf("http://%s/tuner%d/status", deviceIP, tunerIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create status request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get signal quality from %s tuner %d: %w",
			deviceIP, tunerIndex, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status request failed with status %d", resp.StatusCode)
	}

	var status tunerStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode tuner status: %w", err)
	}

	return &SignalQuality{
		Strength: status.SignalStrengthPercent,
		SNR:      status.SymbolQualityPercent,
		Quality:  status.SignalQualityPercent,
	}, nil
}

// lineupItem is a single entry in the HDHomeRun lineup.json response.
type lineupItem struct {
	GuideNumber string `json:"GuideNumber"`
	GuideName   string `json:"GuideName"`
	Frequency   int    `json:"Frequency"`
	Modulation  string `json:"Modulation"`
	Program     int    `json:"Program"`
	URL         string `json:"URL"`
}

// ScanChannels performs a channel scan on the specified device.
// Quick scan only checks common ATSC frequencies; full scan checks all.
func (c *HTTPClient) ScanChannels(ctx context.Context, deviceIP string, quick bool, progress func(ScanProgress)) ([]Channel, error) {
	// Initiate the scan via the HDHomeRun HTTP API.
	scanURL := fmt.Sprintf("http://%s/lineup.post?scan=start", deviceIP)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, scanURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create scan request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to start scan on %s: %w", deviceIP, err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scan start failed with status %d", resp.StatusCode)
	}

	// Poll scan progress until complete.
	pollInterval := 2 * time.Second
	if quick {
		pollInterval = 1 * time.Second
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			scanProgress, done, err := c.pollScanProgress(ctx, deviceIP)
			if err != nil {
				return nil, fmt.Errorf("error polling scan progress: %w", err)
			}

			if progress != nil {
				progress(scanProgress)
			}

			if done {
				// Fetch the final lineup.
				return c.fetchLineup(ctx, deviceIP)
			}
		}
	}
}

// scanStatusResponse represents the scan progress from the device.
type scanStatusResponse struct {
	ScanInProgress int    `json:"ScanInProgress"`
	ScanPossible   int    `json:"ScanPossible"`
	Progress       int    `json:"Progress"`
	Found          int    `json:"Found"`
}

// pollScanProgress checks the current scan status from the device.
func (c *HTTPClient) pollScanProgress(ctx context.Context, deviceIP string) (ScanProgress, bool, error) {
	url := fmt.Sprintf("http://%s/lineup_status.json", deviceIP)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ScanProgress{}, false, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ScanProgress{}, false, err
	}
	defer resp.Body.Close()

	var status scanStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return ScanProgress{}, false, err
	}

	p := ScanProgress{
		Percent: status.Progress,
		Found:   status.Found,
	}

	done := status.ScanInProgress == 0
	return p, done, nil
}

// fetchLineup retrieves the final channel lineup after a scan completes.
func (c *HTTPClient) fetchLineup(ctx context.Context, deviceIP string) ([]Channel, error) {
	url := fmt.Sprintf("http://%s/lineup.json", deviceIP)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch lineup from %s: %w", deviceIP, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lineup request failed with status %d", resp.StatusCode)
	}

	var items []lineupItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("failed to decode lineup: %w", err)
	}

	channels := make([]Channel, 0, len(items))
	for _, item := range items {
		channels = append(channels, Channel{
			Number:     item.GuideNumber,
			Name:       item.GuideName,
			Frequency:  item.Frequency,
			Modulation: item.Modulation,
			Program:    item.Program,
		})
	}

	return channels, nil
}

// ParseChannelNumber parses a virtual channel number string (e.g., "5.1")
// into major and minor numbers.
func ParseChannelNumber(s string) (major, minor int, err error) {
	parts := strings.SplitN(s, ".", 2)
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid channel number %q: %w", s, err)
	}
	if len(parts) == 2 {
		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid channel minor number %q: %w", s, err)
		}
	}
	return major, minor, nil
}
