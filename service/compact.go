package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const compactEnvelopePrefix = "codebuddy-compact-v1."

const compactInstruction = `You are compacting a conversation for future continuation. Output only a concise factual summary, not a reply to the user. Preserve the user's goal, important decisions, constraints, files and paths, commands, tool results, errors, unresolved work, and any details needed to continue coding. Do not invent facts. Use plain text.`

type compactState struct {
	Summary   string
	ExpiresAt time.Time
}

var compactStates sync.Map // map[string]compactState, keyed by our response ID

func encodeCompactEnvelope(summary string) string {
	payload, _ := json.Marshal(map[string]any{
		"version": 1,
		"summary": strings.TrimSpace(summary),
	})
	return compactEnvelopePrefix + base64.RawURLEncoding.EncodeToString(payload)
}

func decodeCompactEnvelope(raw string) (string, error) {
	if !strings.HasPrefix(raw, compactEnvelopePrefix) {
		return "", fmt.Errorf("compaction item was not created by this gateway")
	}
	encoded := strings.TrimPrefix(raw, compactEnvelopePrefix)
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("invalid compaction envelope: %w", err)
	}
	var value struct {
		Version int    `json:"version"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", fmt.Errorf("invalid compaction envelope: %w", err)
	}
	if value.Version != 1 || strings.TrimSpace(value.Summary) == "" {
		return "", fmt.Errorf("invalid compaction envelope payload")
	}
	return strings.TrimSpace(value.Summary), nil
}

func rememberCompactState(id, summary string) {
	id = strings.TrimSpace(id)
	summary = strings.TrimSpace(summary)
	if id == "" || summary == "" {
		return
	}
	compactStates.Store(id, compactState{Summary: summary, ExpiresAt: time.Now().Add(2 * time.Hour)})
}

func compactStateFor(id string) (string, bool) {
	value, ok := compactStates.Load(strings.TrimSpace(id))
	if !ok {
		return "", false
	}
	state, ok := value.(compactState)
	if !ok || time.Now().After(state.ExpiresAt) {
		compactStates.Delete(id)
		return "", false
	}
	return state.Summary, true
}

func compactionSummaryFromItem(item map[string]any) (string, error) {
	encrypted := strings.TrimSpace(asString(item["encrypted_content"]))
	if encrypted == "" {
		return "", fmt.Errorf("compaction item is missing encrypted_content")
	}
	return decodeCompactEnvelope(encrypted)
}

func summarySystemMessage(summary string) map[string]any {
	return map[string]any{
		"role":    "system",
		"content": "Previous conversation summary from the gateway:\n\n" + strings.TrimSpace(summary),
	}
}
