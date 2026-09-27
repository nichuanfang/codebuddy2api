package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"codebuddy-gateway/global"

	"go.uber.org/zap"
)

const (
	responseHistoryTTL      = 2 * time.Hour
	responseHistoryMaxItems = 512
	responseHistoryMaxEntry = 1 << 20
	responseHistoryMaxBytes = 64 << 20
)

type responseHistoryScope struct {
	key        string
	hasSession bool
}

func historyScopeFor(headers http.Header, queryKey string, raw []byte) responseHistoryScope {
	credential := firstHeader(headers, "Authorization", "api-key", "X-Api-Key")
	if credential == "" {
		credential = queryKey
	}
	credential = strings.TrimSpace(credential)
	if len(credential) >= 7 && strings.EqualFold(credential[:7], "bearer ") {
		credential = strings.TrimSpace(credential[7:])
	}
	session := firstHeader(headers, "X-Session-Id", "X-Conversation-Id")
	if session == "" {
		var body map[string]any
		if json.Unmarshal(raw, &body) == nil {
			for _, name := range []string{"prompt_cache_key", "conversation_id", "session_id"} {
				if session = strings.TrimSpace(asString(body[name])); session != "" {
					break
				}
			}
		}
	}
	// The configured gateway key may be shared by trusted clients. Never use
	// the first user message as a session identity for tool-call fallback.
	sum := sha256.Sum256([]byte(credential + "\x00" + session))
	return responseHistoryScope{key: hex.EncodeToString(sum[:]), hasSession: session != ""}
}

type cachedToolResponse struct {
	key       string
	scope     responseHistoryScope
	items     []any
	calls     map[string]map[string]any
	bytes     int
	expiresAt time.Time
}

type responseHistoryStore struct {
	mu       sync.Mutex
	entries  map[string]*cachedToolResponse
	order    []string
	bytes    int
	now      func() time.Time
	maxItems int
	maxEntry int
	maxBytes int
}

func newResponseHistoryStore() *responseHistoryStore {
	return &responseHistoryStore{
		entries: make(map[string]*cachedToolResponse), now: time.Now,
		maxItems: responseHistoryMaxItems, maxEntry: responseHistoryMaxEntry, maxBytes: responseHistoryMaxBytes,
	}
}

func historyKey(scope responseHistoryScope, id string) string { return scope.key + ":" + id }

func (s *responseHistoryStore) removeLocked(key string) {
	if entry := s.entries[key]; entry != nil {
		s.bytes -= entry.bytes
		delete(s.entries, key)
	}
}

func (s *responseHistoryStore) pruneLocked() {
	for _, key := range s.order {
		if entry := s.entries[key]; entry != nil && !s.now().Before(entry.expiresAt) {
			s.removeLocked(key)
		}
	}
	kept := s.order[:0]
	for _, key := range s.order {
		if s.entries[key] != nil {
			kept = append(kept, key)
		}
	}
	s.order = kept
}

// record stores only completed, executable tool calls. Neither partial output
// nor arbitrary conversation text becomes cross-request server-side state.
func (s *responseHistoryStore) record(response map[string]any, scope responseHistoryScope) {
	if s == nil || asString(response["status"]) != "completed" {
		return
	}
	id := strings.TrimSpace(asString(response["id"]))
	output, _ := response["output"].([]any)
	if id == "" || len(output) == 0 {
		return
	}
	var reasoning, calls []any
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		switch asString(item["type"]) {
		case "reasoning":
			reasoning = append(reasoning, item)
		case "function_call", "custom_tool_call":
			if asString(item["status"]) == "completed" &&
				strings.TrimSpace(asString(item["call_id"])) != "" &&
				strings.TrimSpace(asString(item["name"])) != "" {
				calls = append(calls, item)
			}
		}
	}
	if len(calls) == 0 {
		return
	}
	// JSON round trip freezes the stored items and detaches them from the
	// response adapter, which may still own its output maps.
	encoded, err := json.Marshal(append(reasoning, calls...))
	if err != nil {
		return
	}
	if len(encoded) > s.maxEntry || len(encoded) > s.maxBytes {
		if global.CORE_LOG != nil {
			global.CORE_LOG.Warn("Responses tool history entry exceeds cache limit", zap.Int("bytes", len(encoded)))
		}
		return
	}
	var items []any
	if json.Unmarshal(encoded, &items) != nil {
		return
	}
	entry := &cachedToolResponse{
		key: historyKey(scope, id), scope: scope, items: items,
		calls: make(map[string]map[string]any), bytes: len(encoded), expiresAt: s.now().Add(responseHistoryTTL),
	}
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item != nil && isCachedCallType(asString(item["type"])) {
			entry.calls[asString(item["call_id"])] = item
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if s.entries[entry.key] != nil {
		s.removeLocked(entry.key)
		for i, key := range s.order {
			if key == entry.key {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
	}
	for (len(s.entries) >= s.maxItems || s.bytes+entry.bytes > s.maxBytes) && len(s.order) > 0 {
		s.removeLocked(s.order[0])
		s.order = s.order[1:]
	}
	s.entries[entry.key] = entry
	s.order = append(s.order, entry.key)
	s.bytes += entry.bytes
}

func isCachedCallType(typ string) bool { return typ == "function_call" || typ == "custom_tool_call" }
func isToolOutputType(typ string) bool {
	return typ == "function_call_output" || typ == "custom_tool_call_output" || typ == "tool_result"
}

func responseItemCallID(item map[string]any) string {
	if id := asString(item["call_id"]); id != "" {
		return id
	}
	return asString(item["tool_use_id"])
}

// snapshot returns independent copies, so no request mutates cached history.
func (s *responseHistoryStore) snapshot(scope responseHistoryScope, previousID string) (*cachedToolResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	entry := s.entries[historyKey(scope, previousID)]
	if entry == nil {
		return nil, false
	}
	return cloneCachedResponse(entry), true
}

func cloneCachedResponse(entry *cachedToolResponse) *cachedToolResponse {
	encoded, _ := json.Marshal(entry.items)
	var items []any
	_ = json.Unmarshal(encoded, &items)
	copyEntry := *entry
	copyEntry.items = items
	copyEntry.calls = make(map[string]map[string]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item != nil && isCachedCallType(asString(item["type"])) {
			copyEntry.calls[asString(item["call_id"])] = item
		}
	}
	return &copyEntry
}

func (s *responseHistoryStore) uniqueCall(scope responseHistoryScope, callID string) *cachedToolResponse {
	if !scope.hasSession {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	var found *cachedToolResponse
	for _, key := range s.order {
		entry := s.entries[key]
		if entry == nil || entry.scope != scope || entry.calls[callID] == nil {
			continue
		}
		if found != nil {
			return nil // ambiguous call_id: never guess across responses
		}
		found = entry
	}
	if found == nil {
		return nil
	}
	return cloneCachedResponse(found)
}

// enrich adds original assistant call items before tool outputs. It deliberately
// does not reconstruct the rest of a conversation from previous_response_id.
func (s *responseHistoryStore) enrich(raw []byte, scope responseHistoryScope) ([]byte, error) {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return raw, nil // Preserve the existing invalid-JSON error path.
	}
	previousID := strings.TrimSpace(asString(body["previous_response_id"]))
	var previous *cachedToolResponse
	if previousID != "" {
		previous, _ = s.snapshot(scope, previousID)
		if _, compact := compactStateFor(previousID); !compact {
			delete(body, "previous_response_id")
			if previous == nil && global.CORE_LOG != nil {
				global.CORE_LOG.Warn("Responses previous response history unavailable", zap.String("response_id", clipText(previousID, 100)))
			}
		}
	}
	input, array := body["input"].([]any)
	if !array {
		if item, ok := body["input"].(map[string]any); ok {
			input = []any{item}
		} else {
			return json.Marshal(body)
		}
	}
	requested := make(map[string]bool)
	existing := make(map[string]bool)
	presentItemIDs := make(map[string]bool)
	for _, rawItem := range input {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}
		if id := asString(item["id"]); id != "" {
			presentItemIDs[id] = true
		}
		typ := asString(item["type"])
		if isToolOutputType(typ) {
			requested[responseItemCallID(item)] = true
		} else if isCachedCallType(typ) {
			existing[responseItemCallID(item)] = true
		}
	}
	if len(requested) == 0 {
		return json.Marshal(body)
	}
	seen := make(map[string]bool)
	insertedReasoning := make(map[string]bool)
	out := make([]any, 0, len(input)+len(requested)+2)
	appendCalls := func(source *cachedToolResponse) {
		if source == nil {
			return
		}
		missing := false
		for _, rawItem := range source.items {
			item, _ := rawItem.(map[string]any)
			if item != nil && isCachedCallType(asString(item["type"])) {
				id := responseItemCallID(item)
				if requested[id] && !existing[id] && !seen[id] {
					missing = true
				}
			}
		}
		if !missing {
			return
		}
		if !insertedReasoning[source.key] {
			for _, rawItem := range source.items {
				item, _ := rawItem.(map[string]any)
				if item != nil && asString(item["type"]) == "reasoning" && !presentItemIDs[asString(item["id"])] {
					out = append(out, item)
				}
			}
			insertedReasoning[source.key] = true
		}
		for _, rawItem := range source.items {
			item, _ := rawItem.(map[string]any)
			if item == nil || !isCachedCallType(asString(item["type"])) {
				continue
			}
			id := responseItemCallID(item)
			if requested[id] && !existing[id] && !seen[id] {
				out = append(out, item)
				seen[id] = true
			}
		}
	}
	groupInserted := false
	for _, rawItem := range input {
		item, _ := rawItem.(map[string]any)
		if item != nil && isCachedCallType(asString(item["type"])) {
			seen[responseItemCallID(item)] = true
		}
		if item != nil && isToolOutputType(asString(item["type"])) {
			id := responseItemCallID(item)
			if id != "" && !groupInserted && previous != nil && previous.calls[id] != nil {
				appendCalls(previous)
				groupInserted = true
			}
			if id != "" && !seen[id] {
				appendCalls(s.uniqueCall(scope, id))
			}
			if id != "" && !seen[id] {
				return nil, fmt.Errorf("Responses tool output call_id %q has no matching call item", clipText(id, 100))
			}
		}
		out = append(out, rawItem)
	}
	body["input"] = out
	return json.Marshal(body)
}
