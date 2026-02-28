// Package cec provides HDMI-CEC control via cec-client for AntBox.
//
// CEC (Consumer Electronics Control) allows AntBox to control the TV and
// other devices on the HDMI bus: volume, power, input switching, standby.
// This package wraps cec-client (libcec) with an HTTP API so the Owl web
// app and owlOS UI can send remote commands.
//
// Routes (registered by main.go or the HTTP server wrapper):
//   GET  /cec/devices   — discover devices on the bus
//   POST /cec/command   — send a CEC command to a device
//
// When cec-client is not installed (dev mode / CI), both handlers return
// mock data so the web UI can be developed without real hardware.
package cec

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
)

// Device represents a CEC device discovered on the HDMI bus.
type Device struct {
	// Address is the CEC logical address, e.g. "0" for the TV, "4" for Playback.
	Address string `json:"address"`
	// Name is the OSD name reported by the device, e.g. "Samsung TV".
	Name string `json:"name"`
	// Type is one of: TV, Recorder, Tuner, Playback, AudioSystem, Switch, VideoProcessor.
	Type string `json:"type"`
	// Active is true when this device is the current active source.
	Active bool `json:"active"`
}

// Command is the JSON body for POST /cec/command.
type Command struct {
	// DeviceAddr is the CEC logical address of the target device, e.g. "0" for TV.
	DeviceAddr string `json:"device_addr"`
	// Op is the operation to perform.
	// Valid values: volume_up, volume_down, mute, standby, power_on, active_source,
	//               set_input, osd_name.
	Op string `json:"op"`
	// Value is an optional string argument (used by set_input, osd_name).
	Value string `json:"value,omitempty"`
}

// CommandResult is returned by POST /cec/command.
type CommandResult struct {
	Status string `json:"status"` // "sent" or "error"
	Note   string `json:"note,omitempty"`
}

// HandleDiscoverDevices handles GET /cec/devices.
// It runs cec-client to enumerate devices on the HDMI bus and returns them
// as a JSON array. If cec-client is not available, it returns mock devices
// so the UI can be developed without real hardware.
func HandleDiscoverDevices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	devices, err := discoverDevices()
	if err != nil {
		// cec-client unavailable — return mock devices for dev/CI.
		devices = mockDevices()
	}

	if err := json.NewEncoder(w).Encode(devices); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
	}
}

// HandleSendCommand handles POST /cec/command.
// It parses the Command JSON body and dispatches a cec-client command.
// CEC is best-effort: if cec-client fails the response is still 200 with
// a note field — the caller should not treat CEC errors as fatal.
func HandleSendCommand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var cmd Command
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(CommandResult{Status: "error", Note: "invalid JSON body"})
		return
	}

	cecLine, err := opToCECLine(cmd)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(CommandResult{Status: "error", Note: err.Error()})
		return
	}

	execErr := runCECClient(cecLine)
	if execErr != nil {
		// Best-effort: log but respond with success + note.
		json.NewEncoder(w).Encode(CommandResult{
			Status: "sent",
			Note:   "cec-client unavailable; command not delivered to hardware",
		})
		return
	}

	json.NewEncoder(w).Encode(CommandResult{Status: "sent"})
}

// opToCECLine converts a Command to the cec-client command string.
// Reference: cec-client interactive commands (libcec documentation).
func opToCECLine(cmd Command) (string, error) {
	addr := cmd.DeviceAddr
	if addr == "" {
		addr = "0" // default to TV
	}

	switch cmd.Op {
	case "volume_up":
		return "vol up", nil
	case "volume_down":
		return "vol down", nil
	case "mute":
		return "mute", nil
	case "standby":
		// "standby <addr>" — puts device at logical address into standby.
		return fmt.Sprintf("standby %s", addr), nil
	case "power_on":
		// "on <addr>" — wakes device from standby.
		return fmt.Sprintf("on %s", addr), nil
	case "active_source":
		// "as" — announce AntBox as the active source (switches TV input).
		return "as", nil
	case "set_input":
		// No direct cec-client command; active_source is the closest equivalent.
		// In production, use the raw CEC opcode ActiveSource (0x82) via tx.
		if cmd.Value == "" {
			return "", fmt.Errorf("set_input requires a value (physical address, e.g. 1.0.0.0)")
		}
		return fmt.Sprintf("tx 4F:82:%s", physAddrToHex(cmd.Value)), nil
	case "osd_name":
		if cmd.Value == "" {
			return "", fmt.Errorf("osd_name requires a value (name string)")
		}
		return fmt.Sprintf("osd %s %s", addr, cmd.Value), nil
	default:
		return "", fmt.Errorf("unknown op %q; valid: volume_up, volume_down, mute, standby, power_on, active_source, set_input, osd_name", cmd.Op)
	}
}

// runCECClient pipes cecLine into cec-client -s -d 1 (single command, no log spam).
func runCECClient(cecLine string) error {
	c := exec.Command("cec-client", "-s", "-d", "1")
	c.Stdin = strings.NewReader(cecLine + "\n")
	return c.Run()
}

// discoverDevices runs cec-client -l to list devices.
// Output format varies by libcec version; this parser handles the common
// "device #N: address=X.X.X.X name=Y type=Z" format.
func discoverDevices() ([]Device, error) {
	out, err := exec.Command("cec-client", "-l").Output()
	if err != nil {
		return nil, err
	}

	var devices []Device
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "device #") {
			continue
		}
		// Minimal parser — production would use a more robust approach.
		d := Device{
			Address: extractField(line, "address"),
			Name:    extractField(line, "name"),
			Type:    extractField(line, "type"),
		}
		if d.Address == "" {
			d.Address = "0"
		}
		if d.Type == "" {
			d.Type = "TV"
		}
		devices = append(devices, d)
	}

	return devices, nil
}

// extractField extracts key=value from a cec-client -l output line.
func extractField(line, key string) string {
	marker := key + "="
	idx := strings.Index(line, marker)
	if idx == -1 {
		return ""
	}
	rest := line[idx+len(marker):]
	// Value ends at next space or end of string.
	if sp := strings.Index(rest, " "); sp != -1 {
		return rest[:sp]
	}
	return rest
}

// physAddrToHex converts a CEC physical address "1.2.3.4" to the two-byte
// hex representation used in raw CEC opcodes, e.g. "10:00".
// This is a best-effort helper — production should validate the address.
func physAddrToHex(addr string) string {
	parts := strings.Split(addr, ".")
	if len(parts) != 4 {
		return "10:00"
	}
	// CEC physical address nibble encoding: each part is one nibble.
	return fmt.Sprintf("%s%s:%s%s", parts[0], parts[1], parts[2], parts[3])
}

// mockDevices returns a static list of mock devices for dev/CI environments
// where cec-client is not installed.
func mockDevices() []Device {
	return []Device{
		{Address: "0", Name: "Mock TV", Type: "TV", Active: false},
		{Address: "4", Name: "AntBox", Type: "Playback", Active: true},
		{Address: "5", Name: "Soundbar", Type: "AudioSystem", Active: false},
	}
}
