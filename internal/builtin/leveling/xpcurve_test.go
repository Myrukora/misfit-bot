package leveling

import "testing"

func TestLevelForXP(t *testing.T) {
	cases := []struct {
		name     string
		xp       int64
		maxLevel int
		level    int
		progress int64
		next     int64
	}{
		// MEE6 curve: 100, 155, 220, 295 XP per level. Floors: 1=100, 2=255, 3=475.
		{name: "just below level 1", xp: 99, maxLevel: 100, level: 0, progress: 99, next: 100},
		{name: "exactly level 1", xp: 100, maxLevel: 100, level: 1, progress: 0, next: 155},
		{name: "inside level 1", xp: 254, maxLevel: 100, level: 1, progress: 154, next: 155},
		{name: "exactly level 2", xp: 255, maxLevel: 100, level: 2, progress: 0, next: 220},
		{name: "exactly level 3", xp: 475, maxLevel: 100, level: 3, progress: 0, next: 295},
		{name: "zero xp", xp: 0, maxLevel: 500, level: 0, progress: 0, next: 100},
		{name: "clamped at max level", xp: 1_000_000, maxLevel: 2, level: 2, progress: 0, next: 0},
		{name: "exactly at max level floor", xp: 770, maxLevel: 3, level: 3, progress: 0, next: 0},
		{name: "max level one", xp: 100, maxLevel: 1, level: 1, progress: 0, next: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			level, progress, next := levelForXP(tc.xp, tc.maxLevel)
			if level != tc.level || progress != tc.progress || next != tc.next {
				t.Fatalf("levelForXP(%d, %d) = (%d, %d, %d), want (%d, %d, %d)",
					tc.xp, tc.maxLevel, level, progress, next, tc.level, tc.progress, tc.next)
			}
		})
	}
}

func TestFormatCompact(t *testing.T) {
	cases := map[int64]string{
		0:         "0",
		999:       "999",
		1000:      "1.0K",
		1100:      "1.1K",
		7100:      "7.1K",
		12000:     "12K",
		999999:    "1000K",
		1200000:   "1.2M",
		12500000:  "13M",
		100000000: "100M",
	}
	for in, want := range cases {
		if got := formatCompact(in); got != want {
			t.Errorf("formatCompact(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatVoice(t *testing.T) {
	cases := map[int64]string{
		0:     "0m",
		59:    "0m",
		2700:  "45m",
		3500:  "58m",
		3600:  "1h",
		12000: "3h 20m",
	}
	for in, want := range cases {
		if got := formatVoice(in); got != want {
			t.Errorf("formatVoice(%d) = %q, want %q", in, got, want)
		}
	}
}
