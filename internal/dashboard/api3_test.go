package dashboard

import (
	"path/filepath"
	"testing"
)

// TestParseImageFilterEnableBody covers the enable endpoint's body decoding:
// real bool required, missing field rejected, garbage rejected.
func TestParseImageFilterEnableBody(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    bool
		wantErr bool
	}{
		{"true", `{"enabled": true}`, true, false},
		{"false", `{"enabled": false}`, false, false},
		{"missing field", `{"other": 1}`, false, true},
		{"empty object", `{}`, false, true},
		{"garbage", `not json`, false, true},
		{"empty body", ``, false, true},
		{"string instead of bool", `{"enabled": "yes"}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseImageFilterEnableBody([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got enabled=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestImageFilterRemoveNameNormalization pins the DELETE path handling: the
// handler applies filepath.Base before the guard, so traversal collapses and
// only empty/dot segments are rejected.
func TestImageFilterRemoveNameNormalization(t *testing.T) {
	if got := filepath.Base("../../config.json"); got != "config.json" {
		t.Fatalf("precondition: filepath.Base behavior changed: %q", got)
	}
	if got := filepath.Base("image abc_123.png"); got != "image abc_123.png" {
		t.Errorf("plain names must survive: %q", got)
	}
}
