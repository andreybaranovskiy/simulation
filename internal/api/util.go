package api

import (
	"errors"
	"runtime"
	"strconv"
	"strings"

	"github.com/andreybaranovskiy/simulation/internal/store"
)

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

func isForeignKey(err error) bool { return errors.Is(err, store.ErrForeignKey) }

// stackTrace captures the goroutine stack for a panic log, capped so a deep
// recursion cannot write megabytes into the log file.
func stackTrace() string {
	buf := make([]byte, 8<<10)
	n := runtime.Stack(buf, false)
	return string(buf[:n])
}

// queryInt reads an integer query parameter, falling back to def and clamping
// to [min, max] so a hand-edited URL cannot ask for a million rows.
func queryInt(raw string, def, min, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// queryBool reads a boolean query parameter, treating a bare flag with no
// value as true.
func queryBool(raw string) bool {
	if raw == "" {
		return false
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return true
	}
	return b
}

// trimTo normalizes free text: collapse surrounding space and enforce a
// length limit that matches the column.
func trimTo(s string, max int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
