package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
)

type ctxKey int

const (
	ctxSession ctxKey = iota
)

// setSession attaches a user session to the request context.
func setSession(ctx context.Context, us *userSession) context.Context {
	return context.WithValue(ctx, ctxSession, us)
}

// sessionOf returns the authenticated session from the request context, or nil.
func sessionOf(r *http.Request) *userSession {
	if v, ok := r.Context().Value(ctxSession).(*userSession); ok {
		return v
	}
	return nil
}

// writeJSON encodes v as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// randHex returns n bytes encoded as a 2*n-char lowercase hex string.
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sortedKeys returns the map keys in sorted order.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
