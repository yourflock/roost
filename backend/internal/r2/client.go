// Package r2 provides a minimal Cloudflare R2 (S3-compatible) client using only
// the Go standard library. It implements AWS Signature Version 4 for authentication.
//
// Required environment variables:
//   R2_ENDPOINT   — https://{account_id}.r2.cloudflarestorage.com
//   R2_ACCESS_KEY — R2 API token access key ID
//   R2_SECRET_KEY — R2 API token secret access key
//
// If any variable is unset, New() returns an error and callers should degrade
// gracefully (log a warning, skip the upload, store a placeholder URL).
package r2

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client holds R2 credentials and the endpoint URL.
type Client struct {
	endpoint   string // https://{account_id}.r2.cloudflarestorage.com
	accessKey  string
	secretKey  string
	httpClient *http.Client
}

// New reads R2 credentials from environment variables and returns a Client.
// Returns an error if any required variable is missing or empty.
func New() (*Client, error) {
	endpoint := os.Getenv("R2_ENDPOINT")
	accessKey := os.Getenv("R2_ACCESS_KEY")
	secretKey := os.Getenv("R2_SECRET_KEY")

	if endpoint == "" {
		return nil, fmt.Errorf("r2: R2_ENDPOINT is not set (expected https://{account_id}.r2.cloudflarestorage.com)")
	}
	if accessKey == "" {
		return nil, fmt.Errorf("r2: R2_ACCESS_KEY is not set")
	}
	if secretKey == "" {
		return nil, fmt.Errorf("r2: R2_SECRET_KEY is not set")
	}

	// Normalise: strip trailing slash
	endpoint = strings.TrimRight(endpoint, "/")

	return &Client{
		endpoint:   endpoint,
		accessKey:  accessKey,
		secretKey:  secretKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// PutObject uploads data to the given bucket and key with the specified content type.
// Returns the public object URL (using the endpoint base) or an error.
func (c *Client) PutObject(bucket, key string, data []byte, contentType string) (string, error) {
	if bucket == "" || key == "" {
		return "", fmt.Errorf("r2: bucket and key must not be empty")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	objectURL := fmt.Sprintf("%s/%s/%s", c.endpoint, bucket, key)

	// Build the signed request.
	req, err := c.newSignedRequest(http.MethodPut, bucket, key, contentType, data)
	if err != nil {
		return "", fmt.Errorf("r2: failed to build signed request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("r2: HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated &&
		resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("r2: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return objectURL, nil
}

// newSignedRequest builds an HTTP PUT request signed with AWS Signature Version 4.
func (c *Client) newSignedRequest(method, bucket, key, contentType string, body []byte) (*http.Request, error) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	// Extract the hostname from the endpoint (everything after "https://").
	host := c.endpoint
	if idx := strings.Index(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}

	url := fmt.Sprintf("%s/%s/%s", c.endpoint, bucket, key)

	// Payload hash
	payloadHash := hexSHA256(body)

	// Canonical headers (must be sorted alphabetically by header name)
	canonicalHeaders := fmt.Sprintf(
		"content-type:%s\nhost:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		contentType, host, payloadHash, amzDate,
	)
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"

	// Canonical request
	canonicalRequest := strings.Join([]string{
		method,
		"/" + bucket + "/" + key,
		"", // no query string
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// String to sign
	credentialScope := fmt.Sprintf("%s/auto/s3/aws4_request", dateStamp)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	// Signing key
	signingKey := deriveSigningKey(c.secretKey, dateStamp, "auto", "s3")
	signature := hexHMAC(signingKey, []byte(stringToSign))

	// Authorization header
	authorization := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		c.accessKey, credentialScope, signedHeaders, signature,
	)

	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Authorization", authorization)
	req.ContentLength = int64(len(body))

	return req, nil
}

// ── AWS Sig V4 helpers ────────────────────────────────────────────────────────

func hexSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hexHMAC(key, data []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

func rawHMAC(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// deriveSigningKey produces the AWS V4 signing key for a given date, region, and service.
// For Cloudflare R2, region is "auto" and service is "s3".
func deriveSigningKey(secret, date, region, service string) []byte {
	kDate := rawHMAC([]byte("AWS4"+secret), []byte(date))
	kRegion := rawHMAC(kDate, []byte(region))
	kService := rawHMAC(kRegion, []byte(service))
	kSigning := rawHMAC(kService, []byte("aws4_request"))
	return kSigning
}
