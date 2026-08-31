package dashboard

import "testing"

func TestVersionDisplay(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"0.1.0", "v0.1.0"},
		{"v0.1.0", "v0.1.0"},
		{"0.2.0-rc.1", "v0.2.0-rc.1"},
		{"1.0.0+build.5", "v1.0.0"}, // build metadata is not part of the version
		{"dev", "dev"},
		{"", "dev"},       // never stamped
		{"nonsense", "—"}, // garbage never renders as a version
		{"1.2.3.4", "—"},  // ditto
	} {
		if got := versionDisplay(tc.in); got != tc.want {
			t.Errorf("versionDisplay(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVersionLabel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current string
		latest  string
		pending bool
		want    string
	}{
		{name: "current release", current: "0.1.0", latest: "0.1.0", want: "v0.1.0"},
		{name: "release pending", current: "0.1.0", latest: "0.2.0", pending: true, want: "v0.1.0 → v0.2.0"},
		{name: "nothing cached", current: "0.1.0", want: "v0.1.0"},
		{name: "pending flag without a tag", current: "0.1.0", pending: true, want: "v0.1.0"},
		{name: "pending but not actually ahead", current: "0.2.0", latest: "0.2.0", pending: true, want: "v0.2.0"},
		{name: "unstamped build", current: "dev", latest: "0.2.0", pending: false, want: "dev"},
	} {
		if got := versionLabel(tc.current, tc.latest, tc.pending); got != tc.want {
			t.Errorf("%s: versionLabel(%q, %q, %v) = %q, want %q", tc.name, tc.current, tc.latest, tc.pending, got, tc.want)
		}
	}
}
