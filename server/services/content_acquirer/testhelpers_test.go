// testhelpers_test.go — test helpers for content_acquirer.
package content_acquirer

import (
	"log/slog"
	"os"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, nil))
}
