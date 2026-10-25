package dashboard

import (
	"testing"
	"time"
)

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"negative", -time.Second, "0s"},
		{"zero", 0, "0s"},
		{"seconds", 59 * time.Second, "59s"},
		{"minute", time.Minute, "1m0s"},
		{"max minutes", 59*time.Minute + 59*time.Second, "59m59s"},
		{"hour", time.Hour, "1h0m"},
		{"hours", 10*time.Hour + 44*time.Minute + 34*time.Second, "10h44m"},
		{"day", 24 * time.Hour, "1d0h"},
		{"max days", 364*24*time.Hour + 23*time.Hour, "364d23h"},
		{"year", 365 * 24 * time.Hour, "1y0d"},
		{"years", 2*365*24*time.Hour + 3*24*time.Hour, "2y3d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatUptime(tc.d); got != tc.want {
				t.Fatalf("formatUptime(%v) = %q, want %q", tc.d, got, tc.want)
			}
			if len(tc.want) > 8 {
				t.Fatalf("formatted uptime %q is too long for the stat card", tc.want)
			}
		})
	}
}
