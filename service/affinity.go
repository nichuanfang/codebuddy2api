package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// RequestAffinityKey returns a privacy-preserving caller/session key. It only
// returns a key when a stable session identifier is available; the fallback to
// the first user message is intentionally best-effort for clients that expose
// no conversation ID.
func RequestAffinityKey(headers http.Header, body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	identityType, identity := requestSessionIdentity(headers, payload)
	if identity == "" {
		return ""
	}
	modelName := asString(payload["model"])
	caller := firstHeader(headers, "Authorization", "X-Api-Key", "api-key")
	callerHash := sha256.Sum256([]byte(strings.TrimSpace(caller)))

	seed := struct {
		CallerHash  string `json:"caller_hash"`
		Model       string `json:"model"`
		IdentityTyp string `json:"identity_type"`
		Identity    string `json:"identity"`
	}{
		CallerHash:  hex.EncodeToString(callerHash[:]),
		Model:       modelName,
		IdentityTyp: identityType,
		Identity:    identity,
	}
	raw, _ := json.Marshal(seed)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func requestSessionIdentity(headers http.Header, payload map[string]any) (string, string) {
	for _, name := range []string{"X-Session-Id", "X-Conversation-Id"} {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return strings.ToLower(name), value
		}
	}
	for _, name := range []string{"prompt_cache_key", "conversation_id", "session_id"} {
		if value := strings.TrimSpace(asString(payload[name])); value != "" {
			return name, value
		}
	}

	messages, ok := payload["messages"].([]any)
	if !ok {
		messages, _ = payload["input"].([]any)
	}
	if text := firstUserContent(messages); text != "" {
		return "first_user", text
	}
	if text := asString(payload["input"]); text != "" {
		return "first_user", text
	}
	return "", ""
}

func firstUserContent(messages []any) string {
	for _, item := range messages {
		m, _ := item.(map[string]any)
		if m == nil || strings.ToLower(strings.TrimSpace(asString(m["role"]))) != "user" {
			continue
		}
		content := m["content"]
		if content == nil {
			content = m["input"]
		}
		if raw, err := json.Marshal(content); err == nil && string(raw) != "null" && string(raw) != `""` {
			return string(raw)
		}
	}
	return ""
}

func firstHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}
