package service

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRequestAffinityKeyIsStableAndScoped(t *testing.T) {
	headers := http.Header{"X-Session-Id": []string{"session-1"}, "Authorization": []string{"Bearer secret"}}
	body := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hello"}]}`)
	first := RequestAffinityKey(headers, body)
	if first == "" || first != RequestAffinityKey(headers, body) {
		t.Fatalf("unstable key: %q", first)
	}
	if strings.Contains(first, "secret") || strings.Contains(first, "hello") {
		t.Fatalf("raw identity leaked: %q", first)
	}
	otherSession := http.Header{"X-Session-Id": []string{"session-2"}, "Authorization": []string{"Bearer secret"}}
	if first == RequestAffinityKey(otherSession, body) {
		t.Fatal("different sessions shared affinity key")
	}
	otherCaller := http.Header{"X-Session-Id": []string{"session-1"}, "Authorization": []string{"Bearer other"}}
	if first == RequestAffinityKey(otherCaller, body) {
		t.Fatal("different callers shared affinity key")
	}
}

func TestRequestAffinityFallsBackToFirstUser(t *testing.T) {
	key := RequestAffinityKey(http.Header{}, []byte(`{"model":"glm-5.3","messages":[{"role":"system","content":"rules"},{"role":"user","content":"hello"}]}`))
	if key == "" {
		t.Fatal("first user fallback did not produce a key")
	}
}

func TestSessionBindingCapacityAndExpiry(t *testing.T) {
	r := NewRotator()
	now := time.Now()
	r.sessions["expired"] = sessionBinding{accountID: 1, expiresAt: now.Add(-time.Minute), lastUsed: now.Add(-time.Hour)}
	r.sessions["oldest"] = sessionBinding{accountID: 1, expiresAt: now.Add(time.Hour), lastUsed: now.Add(-time.Hour)}
	if len(r.sessions) != 2 {
		t.Fatal("test setup failed")
	}
	r.pruneSessionsLocked(now)
	if _, ok := r.sessions["expired"]; ok {
		t.Fatal("expired session was not pruned")
	}
}
