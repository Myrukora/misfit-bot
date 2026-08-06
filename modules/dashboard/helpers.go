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

func setSession(ctx context.Context, us *userSession) context.Context {
	return context.WithValue(ctx, ctxSession, us)
}

func sessionOf(r *http.Request) *userSession {
	if v, ok := r.Context().Value(ctxSession).(*userSession); ok {
		return v
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

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

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
