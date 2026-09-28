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

func TestRequestAffinityRecognizesCodexHeaders(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hello"}]}`)

	session := http.Header{}
	session.Set("session-id", "codex-session-1")
	session.Set("Authorization", "Bearer secret")
	key := RequestAffinityKey(session, body)
	if key == "" || key != RequestAffinityKey(session, body) {
		t.Fatalf("unstable Codex session key: %q", key)
	}

	otherSession := http.Header{}
	otherSession.Set("session-id", "codex-session-2")
	otherSession.Set("Authorization", "Bearer secret")
	otherKey := RequestAffinityKey(otherSession, body)
	if key == otherKey {
		t.Fatalf("different Codex sessions shared affinity key: %q %q", key, otherKey)
	}

	otherCaller := http.Header{}
	otherCaller.Set("session-id", "codex-session-1")
	otherCaller.Set("Authorization", "Bearer other")
	if key == RequestAffinityKey(otherCaller, body) {
		t.Fatal("same Codex session crossed caller credentials")
	}

	thread := http.Header{}
	thread.Set("thread-id", "codex-thread-1")
	thread.Set("Authorization", "Bearer secret")
	if key == RequestAffinityKey(thread, body) {
		t.Fatal("thread identity unexpectedly collided with session identity")
	}
	if threadKey := RequestAffinityKey(thread, body); threadKey == "" || threadKey != RequestAffinityKey(thread, body) {
		t.Fatalf("unstable Codex thread key: %q", threadKey)
	}
}

func TestRequestSessionIdentityPrecedence(t *testing.T) {
	headers := http.Header{}
	headers.Set("session-id", "session-1")
	headers.Set("thread-id", "thread-1")
	headers.Set("x-client-request-id", "request-1")
	payload := map[string]any{
		"prompt_cache_key": "cache-key",
		"session_id":       "body-session",
		"conversation_id":  "conversation-1",
	}

	if typ, value := requestSessionIdentity(headers, payload); typ != "session_id" || value != "session-1" {
		t.Fatalf("session precedence: typ=%q value=%q", typ, value)
	}
	headers.Del("session-id")
	if typ, value := requestSessionIdentity(headers, payload); typ != "prompt_cache_key" || value != "cache-key" {
		t.Fatalf("prompt cache precedence: typ=%q value=%q", typ, value)
	}
	delete(payload, "prompt_cache_key")
	if typ, value := requestSessionIdentity(headers, payload); typ != "conversation_id" || value != "conversation-1" {
		t.Fatalf("conversation precedence: typ=%q value=%q", typ, value)
	}
	delete(payload, "conversation_id")
	if typ, value := requestSessionIdentity(headers, payload); typ != "thread_id" || value != "thread-1" {
		t.Fatalf("thread precedence: typ=%q value=%q", typ, value)
	}
}

func TestRequestSessionIdentityReadsCodexHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("session-id", "codex-session-1")
	if typ, value := requestSessionIdentity(headers, map[string]any{}); typ != "session_id" || value != "codex-session-1" {
		t.Fatalf("identity=%q value=%q", typ, value)
	}
}
