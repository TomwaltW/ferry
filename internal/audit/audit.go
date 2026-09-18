// Package audit turns the audit log into what ferry audit export prints
// (spec-p4-b1 3.5): it parses --since and writes the entries as CSV.
package audit

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/TomwaltW/ferry/internal/fsp"
	"github.com/TomwaltW/ferry/internal/ports"
)

// Header is the first CSV line; every row has exactly these columns.
var Header = []string{"at", "kind", "endpoint", "sender_id", "session_id", "detail"}

// TimeFormat is the layout of the at column, always rendered in UTC.
const TimeFormat = "2006-01-02T15:04:05.000Z07:00"

// ErrSince reports a --since value that is not a positive duration.
var ErrSince = errors.New("audit: since must be a positive duration")

// ParseSince reads a --since value: "<n>d" for n days, anything else as
// time.ParseDuration. The result is always above zero.
func ParseSince(s string) (time.Duration, error) {
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseInt(days, 10, 64)
		if err != nil || !allDigits(days) || n <= 0 || n > int64(maxDays) {
			return 0, fmt.Errorf("%w: %q", ErrSince, s)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		d, err = time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", ErrSince, s)
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("%w: %q", ErrSince, s)
	}
	return d, nil
}

// maxDays keeps n*24h inside time.Duration.
const maxDays = int64(1<<63-1) / int64(24*time.Hour)

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// WriteCSV writes the header and one row per entry, in the order given.
func WriteCSV(w io.Writer, entries []ports.AuditEntry) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(Header); err != nil {
		return fmt.Errorf("audit: write csv: %w", err)
	}
	for _, e := range entries {
		if err := cw.Write(Row(e)); err != nil {
			return fmt.Errorf("audit: write csv: %w", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("audit: write csv: %w", err)
	}
	return nil
}

// Row is the CSV record for one entry, in Header order.
func Row(e ports.AuditEntry) []string {
	endpoint := ""
	if e.Endpoint != (fsp.EndpointID{}) {
		endpoint = e.Endpoint.String()
	}
	return []string{
		e.At.UTC().Format(TimeFormat),
		e.Kind,
		endpoint,
		e.SenderID,
		string(e.Session),
		e.Detail,
	}
}
