// upload.go — Higher-level R2 upload helpers built on top of Client.PutObject.
//
// While client.go implements the raw AWS SigV4 PUT, this file adds:
//
//   - UploadFile(bucket, key, localPath)   — read a file from disk and upload
//   - UploadReader(bucket, key, r, ct)     — stream from an io.Reader
//   - UploadHLSDirectory(bucket, prefix, dir) — recursively upload an HLS output dir
//   - PublicURL(bucket, key)               — canonical public URL for an object
//   - MustNew()                            — panic helper for program startup
//
// MIME type detection:
//   - Caller-supplied content type is used when non-empty.
//   - Falls back to mime.TypeByExtension for recognised extensions.
//   - Defaults to "application/octet-stream" when unknown.
//
// All functions are safe for concurrent use (Client is immutable after New()).
package r2

import (
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// UploadFile reads a local file and uploads it to R2 at bucket/key.
// contentType may be empty — the MIME type is inferred from the file extension.
// Returns the public object URL on success.
func (c *Client) UploadFile(bucket, key, localPath string) (string, error) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return "", fmt.Errorf("r2: read %s: %w", localPath, err)
	}
	ct := mimeForPath(localPath)
	return c.PutObject(bucket, key, data, ct)
}

// UploadReader reads all bytes from r and uploads them to R2 at bucket/key.
// contentType may be empty, in which case "application/octet-stream" is used.
// Returns the public object URL on success.
func (c *Client) UploadReader(bucket, key string, r io.Reader, contentType string) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("r2: read reader for %s/%s: %w", bucket, key, err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return c.PutObject(bucket, key, data, contentType)
}

// UploadHLSDirectory recursively uploads every file in dir to R2 under bucket/prefix/.
// Preserves the relative path structure: dir/segment0.ts → bucket/prefix/segment0.ts.
// Typically used after ffmpeg produces an HLS output directory.
// Returns the number of files uploaded and the first error encountered (if any).
// On partial failure, already-uploaded files remain in R2 — callers should clean up
// or retry if they require atomicity.
func (c *Client) UploadHLSDirectory(bucket, prefix, dir string) (int, error) {
	n := 0
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}

		// Derive R2 key relative to the HLS root directory.
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("r2: rel path for %s: %w", path, err)
		}
		key := prefix + "/" + filepath.ToSlash(rel)

		if _, err := c.UploadFile(bucket, key, path); err != nil {
			return fmt.Errorf("r2: upload %s → %s: %w", path, key, err)
		}
		n++
		return nil
	})
	return n, err
}

// PublicURL returns the canonical public URL for an object at bucket/key.
// This is the same URL format returned by PutObject — useful when you already
// know the key and just need the URL without uploading.
func (c *Client) PublicURL(bucket, key string) string {
	return fmt.Sprintf("%s/%s/%s", c.endpoint, bucket, key)
}

// MustNew is a convenience wrapper around New() that panics on error.
// Use at program startup where a misconfigured R2 client is fatal.
func MustNew() *Client {
	c, err := New()
	if err != nil {
		panic(fmt.Sprintf("r2: MustNew failed: %v", err))
	}
	return c
}

// ── MIME helpers ──────────────────────────────────────────────────────────────

// mimeForPath returns the MIME content type for a file path based on its extension.
// Falls back to "application/octet-stream" for unknown types.
func mimeForPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "application/octet-stream"
	}

	// Fast-path for types common in HLS / media pipelines.
	switch ext {
	case ".m3u8":
		return "application/x-mpegurl"
	case ".ts":
		return "video/MP2T"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	case ".ogg":
		return "audio/ogg"
	case ".opus":
		return "audio/ogg; codecs=opus"
	case ".vtt":
		return "text/vtt"
	case ".srt":
		return "text/plain"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	}

	// stdlib fallback for everything else.
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
