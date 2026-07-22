package credhealth

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	warn := 30 * 24 * time.Hour
	ptr := func(d time.Duration) *time.Time { x := now.Add(d); return &x }

	cases := []struct {
		name   string
		expiry *time.Time
		want   Status
	}{
		{"no expiry", nil, StatusNoExpiry},
		{"long future", ptr(365 * 24 * time.Hour), StatusOK},
		{"just beyond warn edge", ptr(warn + time.Second), StatusOK},
		{"exact warn edge", ptr(warn), StatusExpiring},
		{"within warn", ptr(24 * time.Hour), StatusExpiring},
		{"exact expiry instant", ptr(0), StatusExpired},
		{"already expired", ptr(-time.Hour), StatusExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.expiry, now, warn); got != tc.want {
				t.Errorf("classify = %s, want %s", got, tc.want)
			}
		})
	}
}
