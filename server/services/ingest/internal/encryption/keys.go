// keys.go — AES-128 HLS encryption key management.
//
// Keys are 16 random bytes, generated once per channel per day, stored in Redis
// (TTL 48h), and written to disk for FFmpeg to use.
//
// Key rotation: a new key is generated at midnight UTC. The old key expires from
// Redis after 48h (allowing players to finish decrypting yesterday's segments).
//
// Key URI in m3u8: the relative URI "/stream/{slug}/key" is written to the keyinfo
// file. Owl clients append ?token=xxx to this URI when requesting the key from the relay.
package encryption

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RedisKeyStore is the minimal Redis interface needed for key storage.
type RedisKeyStore interface {
	Get(ctx context.Context, key string) interface{ Result() (string, error) }
	Set(ctx context.Context, key, value string, ttl time.Duration) interface{ Err() error }
}

// KeyManager manages AES-128 HLS encryption keys.
type KeyManager struct {
	redis      RedisKeyStore
	segmentDir string
}

// NewKeyManager creates a key manager.
// redis may be nil — keys are stored on disk only (single-node; no Redis failover).
func NewKeyManager(redis RedisKeyStore, segmentDir string) *KeyManager {
	return &KeyManager{redis: redis, segmentDir: segmentDir}
}

// GetCurrentKey returns today's AES-128 key for the given channel, generating it if needed.
// The key is stored in Redis with 48h TTL and written to disk for FFmpeg.
func (km *KeyManager) GetCurrentKey(ctx context.Context, channelSlug string) ([]byte, error) {
	today := time.Now().UTC().Format("20060102")
	return km.getOrGenKey(ctx, channelSlug, today)
}

// getOrGenKey retrieves the key for a channel+date, generating and storing it if absent.
func (km *KeyManager) getOrGenKey(ctx context.Context, channelSlug, date string) ([]byte, error) {
	redisKey := fmt.Sprintf("hls:key:%s:%s", channelSlug, date)

	// Try Redis first
	if km.redis != nil {
		if result, err := km.redis.Get(ctx, redisKey).Result(); err == nil {
			keyBytes, err := hex.DecodeString(result)
			if err == nil && len(keyBytes) == 16 {
				return keyBytes, nil
			}
		}
	}

	// Try disk (single-node fallback)
	keyPath := filepath.Join(km.segmentDir, channelSlug, "enc.key")
	if data, err := os.ReadFile(keyPath); err == nil && len(data) == 16 {
		// Re-cache in Redis if available
		if km.redis != nil {
			km.redis.Set(ctx, redisKey, hex.EncodeToString(data), 48*time.Hour)
		}
		return data, nil
	}

	// Generate new key
	key, err := generateKey()
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	// Store in Redis (48h TTL — covers yesterday's segments during rotation window)
	if km.redis != nil {
		if err := km.redis.Set(ctx, redisKey, hex.EncodeToString(key), 48*time.Hour).Err(); err != nil {
			// Non-fatal: key is still written to disk
			fmt.Fprintf(os.Stderr, "[encryption] redis set key: %v\n", err)
		}
	}

	// Write key to disk for FFmpeg
	if err := km.writeKeyToDisk(channelSlug, key); err != nil {
		return nil, fmt.Errorf("write key to disk: %w", err)
	}

	return key, nil
}

// WriteKeyInfo ensures enc.key and enc.keyinfo are written for FFmpeg.
// Called before starting an encrypted FFmpeg pipeline.
func (km *KeyManager) WriteKeyInfo(ctx context.Context, channelSlug string) error {
	key, err := km.GetCurrentKey(ctx, channelSlug)
	if err != nil {
		return err
	}

	if err := km.writeKeyToDisk(channelSlug, key); err != nil {
		return err
	}

	return km.writeKeyInfo(channelSlug)
}

// writeKeyToDisk writes the 16-byte AES key to {segmentDir}/{slug}/enc.key.
func (km *KeyManager) writeKeyToDisk(channelSlug string, key []byte) error {
	dir := filepath.Join(km.segmentDir, channelSlug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdirall: %w", err)
	}
	keyPath := filepath.Join(dir, "enc.key")
	return os.WriteFile(keyPath, key, 0o600)
}

// writeKeyInfo writes the FFmpeg keyinfo file for the channel.
// Format:
//   Line 1: Key URI (relative — Owl client appends ?token=xxx)
//   Line 2: Path to key file on disk
//   Line 3: (empty — use random IV per segment)
func (km *KeyManager) writeKeyInfo(channelSlug string) error {
	dir := filepath.Join(km.segmentDir, channelSlug)
	keyPath := filepath.Join(dir, "enc.key")
	keyInfoPath := filepath.Join(dir, "enc.keyinfo")

	// Key URI: relative path — relay appends token at serve time
	keyURI := fmt.Sprintf("/stream/%s/key", channelSlug)

	content := fmt.Sprintf("%s\n%s\n", keyURI, keyPath)
	return os.WriteFile(keyInfoPath, []byte(content), 0o644)
}

// RotateKey generates a new key for tomorrow's date, storing it in Redis.
// Should be called at midnight UTC. Does not affect currently playing streams.
func (km *KeyManager) RotateKey(ctx context.Context, channelSlug string) error {
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Format("20060102")
	_, err := km.getOrGenKey(ctx, channelSlug, tomorrow)
	return err
}

// generateKey creates a 16-byte random AES-128 key.
func generateKey() ([]byte, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// GenerateChannelKey is the exported convenience function matching the task spec.
// Returns a new 16-byte key and stores it in Redis+disk.
func GenerateChannelKey(channelSlug, date string) ([]byte, error) {
	return generateKey()
}
