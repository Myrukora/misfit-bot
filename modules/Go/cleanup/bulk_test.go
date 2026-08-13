package main

import (
	"errors"
	"net/http"
	"testing"

	"github.com/disgoorg/disgo/rest"
)

// TestIsBulkDeleteUnsupported locks in the fallback policy: only HTTP 400
// (Discord rejecting the batch itself — messages too old, batch outside 2–100)
// may trigger the single-delete fallback. Permission (403) and rate-limit
// (429) failures must NOT fall back, or the bot would hammer the API with
// individual deletes that fail (or worsen) the same way.
func TestIsBulkDeleteUnsupported(t *testing.T) {
	httpErr := func(status int) error {
		return &rest.Error{Response: &http.Response{StatusCode: status}}
	}
	if !isBulkDeleteUnsupported(httpErr(http.StatusBadRequest)) {
		t.Error("400 should be bulk-unsupported (too old / bad batch)")
	}
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusUnauthorized} {
		if isBulkDeleteUnsupported(httpErr(status)) {
			t.Errorf("%d should NOT trigger the single-delete fallback", status)
		}
	}
	if isBulkDeleteUnsupported(errors.New("network error")) {
		t.Error("non-API errors should not trigger the fallback")
	}
	if isBulkDeleteUnsupported(nil) {
		t.Error("nil should not be bulk-unsupported")
	}
}
