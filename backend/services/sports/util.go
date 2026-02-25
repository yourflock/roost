// util.go — Shared utilities for the sports service.
package sports

import "strconv"

// itos converts an integer to its decimal string representation.
func itos(i int) string {
	return strconv.Itoa(i)
}
