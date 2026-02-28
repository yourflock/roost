// detector_test.go — Unit tests for the abuse detection package.
// P16-T05: Abuse Detection
package abuse_test

import (
	"testing"

	"github.com/yourflock/roost/pkg/abuse"
)

func TestDetectSharedToken_NormalUsage(t *testing.T) {
	// Same IP, same subscriber — not abuse.
	for i := 0; i < 5; i++ {
		detected, event := abuse.DetectSharedToken("10.0.0.1", "subscriber-aaa")
		if detected {
			t.Errorf("iteration %d: false positive — same IP + subscriber should not be flagged", i)
		}
		if event != nil {
			t.Errorf("iteration %d: expected nil event for normal usage", i)
		}
	}
}

func TestDetectSharedToken_TriggersOnThreshold(t *testing.T) {
	// 4 distinct subscribers from the same IP should trigger (threshold=3, so >3 means 4+).
	ip := "203.0.113.42" // Test IP from RFC 5737 documentation range

	detected := false
	for i := 1; i <= 4; i++ {
		subscriberID := "subscriber-shared-" + string(rune('A'+i))
		d, event := abuse.DetectSharedToken(ip, subscriberID)
		if d {
			detected = true
			if event == nil {
				t.Error("expected non-nil AbuseEvent when abuse detected")
			} else {
				if event.Type != "shared_token" {
					t.Errorf("expected event type 'shared_token', got %q", event.Type)
				}
				if event.IP != ip {
					t.Errorf("expected event IP %q, got %q", ip, event.IP)
				}
			}
			break
		}
	}
	if !detected {
		t.Error("expected shared_token detection after 4 distinct subscriber IDs from same IP")
	}
}

func TestDetectSharedToken_DifferentIPs(t *testing.T) {
	// 4 different IPs using different subscribers — not abuse.
	ips := []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4"}
	subscribers := []string{"sub-x1", "sub-x2", "sub-x3", "sub-x4"}
	for i, ip := range ips {
		detected, _ := abuse.DetectSharedToken(ip, subscribers[i])
		if detected {
			t.Errorf("different IPs using different subscribers should not trigger: ip=%s sub=%s",
				ip, subscribers[i])
		}
	}
}

func TestAbuseEventFields(t *testing.T) {
	ip := "198.51.100.99" // Another RFC 5737 documentation IP

	var lastEvent *abuse.AbuseEvent
	for i := 1; i <= 5; i++ {
		sub := "sub-evt-" + string(rune('a'+i))
		detected, event := abuse.DetectSharedToken(ip, sub)
		if detected {
			lastEvent = event
			break
		}
	}
	if lastEvent == nil {
		t.Fatal("expected abuse event to be detected")
	}
	if lastEvent.SubscriberID == "" {
		t.Error("AbuseEvent.SubscriberID should not be empty")
	}
	if lastEvent.IP != ip {
		t.Errorf("AbuseEvent.IP = %q, want %q", lastEvent.IP, ip)
	}
	if lastEvent.DetectedAt.IsZero() {
		t.Error("AbuseEvent.DetectedAt should not be zero")
	}
	if lastEvent.Details == nil {
		t.Error("AbuseEvent.Details should not be nil")
	}
	if _, ok := lastEvent.Details["distinct_subscriber_ids"]; !ok {
		t.Error("expected 'distinct_subscriber_ids' in event details")
	}
}
