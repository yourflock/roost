// Package logrotate provides log rotation for AntBox (T-7H.1.005).
package logrotate

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Config configures log rotation behavior.
type Config struct {
	// LogDir is the directory where log files are written.
	LogDir string
	// MaxSizeBytes is the max log file size before rotation.
	MaxSizeBytes int64
	// MaxFiles is the number of rotated log files to keep (oldest are deleted).
	MaxFiles int
	// FilePrefix is the base name for log files (e.g. "antbox").
	FilePrefix string
}

// DefaultConfig returns the default AntBox log rotation config.
func DefaultConfig(logDir string) Config {
	return Config{
		LogDir:       logDir,
		MaxSizeBytes: 10 * 1024 * 1024, // 10 MB per file
		MaxFiles:     5,                  // keep 5 rotated files
		FilePrefix:   "antbox",
	}
}

// Rotator is a rotating log writer. It implements io.Writer and can be used as
// the output for logrus or any io.Writer-based logger.
type Rotator struct {
	cfg     Config
	mu      sync.Mutex
	current *os.File
	size    int64
}

// New creates a Rotator and opens the initial log file.
func New(cfg Config) (*Rotator, error) {
	if err := os.MkdirAll(cfg.LogDir, 0o755); err != nil {
		return nil, fmt.Errorf("logrotate: create log dir: %w", err)
	}

	r := &Rotator{cfg: cfg}
	if err := r.openNew(); err != nil {
		return nil, err
	}
	return r, nil
}

// Write implements io.Writer. It writes p to the current log file and rotates
// if the file exceeds MaxSizeBytes.
func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.size+int64(len(p)) > r.cfg.MaxSizeBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}

	n, err := r.current.Write(p)
	r.size += int64(n)
	return n, err
}

// Close closes the current log file.
func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}

// currentPath returns the path for the active log file.
func (r *Rotator) currentPath() string {
	return filepath.Join(r.cfg.LogDir, r.cfg.FilePrefix+".log")
}

// openNew opens a fresh log file at the current path.
func (r *Rotator) openNew() error {
	f, err := os.OpenFile(r.currentPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("logrotate: open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("logrotate: stat log file: %w", err)
	}
	r.current = f
	r.size = info.Size()
	return nil
}

// rotate closes the current file, renames it with a timestamp, and opens a new one.
// It also prunes old files beyond MaxFiles.
func (r *Rotator) rotate() error {
	if r.current != nil {
		if err := r.current.Close(); err != nil {
			return fmt.Errorf("logrotate: close: %w", err)
		}
		r.current = nil
	}

	// Rename current → timestamped backup
	ts := time.Now().UTC().Format("20060102-150405")
	rotatedPath := filepath.Join(r.cfg.LogDir, fmt.Sprintf("%s-%s.log", r.cfg.FilePrefix, ts))
	if err := os.Rename(r.currentPath(), rotatedPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("logrotate: rename: %w", err)
	}

	// Prune old files
	r.pruneOld()

	// Open new file
	return r.openNew()
}

// pruneOld removes the oldest rotated log files beyond MaxFiles.
func (r *Rotator) pruneOld() {
	pattern := filepath.Join(r.cfg.LogDir, r.cfg.FilePrefix+"-*.log")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) <= r.cfg.MaxFiles {
		return
	}
	// matches are sorted lexicographically (timestamps are sortable)
	// remove the oldest (first entries)
	toDelete := matches[:len(matches)-r.cfg.MaxFiles]
	for _, path := range toDelete {
		os.Remove(path) //nolint:errcheck — best-effort cleanup
	}
}
