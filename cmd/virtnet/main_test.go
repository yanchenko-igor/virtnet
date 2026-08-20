package main

import (
	"testing"
	"time"
)

func TestParseStart(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Time
		ok   bool
	}{
		{name: "empty defaults to epoch", in: "", want: time.Unix(0, 0), ok: true},
		{name: "unix seconds", in: "1767402000", want: time.Unix(1767402000, 0), ok: true},
		{name: "rfc3339 utc", in: "2026-01-15T08:00:00Z", want: time.Date(2026, time.January, 15, 8, 0, 0, 0, time.UTC), ok: true},
		{name: "rfc3339 offset normalized to utc", in: "2026-01-15T09:00:00+01:00", want: time.Date(2026, time.January, 15, 8, 0, 0, 0, time.UTC), ok: true},
		{name: "rfc3339nano", in: "2026-01-15T08:00:00.123456789Z", want: time.Date(2026, time.January, 15, 8, 0, 0, 123456789, time.UTC), ok: true},
		{name: "garbage rejected", in: "yesterday", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStart(tt.in)
			if (err == nil) != tt.ok {
				t.Fatalf("parseStart(%q) err = %v, ok = %v", tt.in, err, tt.ok)
			}
			if tt.ok && !got.Equal(tt.want) {
				t.Errorf("parseStart(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
