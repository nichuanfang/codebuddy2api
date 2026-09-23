package service

import (
	"encoding/json"
	"strings"
)

// responseToolBinding records the reversible name/type translation for one
// Responses tool. The upstream API only accepts flat function names, while
// Codex may expose namespaced MCP tools and custom tools.
type responseToolBinding struct {
	ClientName    string
	UpstreamName  string
	QualifiedName string
	Kind          string
	InputField    string
}

type responseToolRegistry struct {
	bindings []*responseToolBinding
	aliases  map[string]*responseToolBinding
}

func newResponseToolRegistry(candidates []responseToolCandidate) *responseToolRegistry {
	if len(candidates) == 0 {
		return nil
	}
	counts := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		counts[toolNameKey(candidate.ClientName)]++
	}
	registry := &responseToolRegistry{
		bindings: make([]*responseToolBinding, 0, len(candidates)),
		aliases:  make(map[string]*responseToolBinding, len(candidates)*5),
	}
	for _, candidate := range candidates {
		clientName := candidate.ClientName
		if counts[toolNameKey(candidate.ClientName)] > 1 {
			clientName = candidate.QualifiedName
		}
		binding := &responseToolBinding{
			ClientName:    clientName,
			UpstreamName:  clientName,
			QualifiedName: candidate.QualifiedName,
			Kind:          candidate.Kind,
			InputField:    candidate.InputField,
		}
		registry.bindings = append(registry.bindings, binding)
		registry.addAlias(binding.ClientName, binding)
		registry.addAlias(binding.UpstreamName, binding)
		registry.addAlias(binding.QualifiedName, binding)
		registry.addAlias(candidate.ClientName, binding)
		registry.addAlias("mcp__"+binding.ClientName, binding)
		registry.addAlias("mcp__"+binding.UpstreamName, binding)
		registry.addAlias("mcp__"+binding.QualifiedName, binding)
		registry.addAlias("mcp__"+candidate.ClientName, binding)
	}
	return registry
}

func (r *responseToolRegistry) bindingForCandidate(candidate responseToolCandidate) *responseToolBinding {
	if r == nil {
		return nil
	}
	for _, binding := range r.bindings {
		if binding.QualifiedName == candidate.QualifiedName {
			return binding
		}
	}
	return nil
}

func (r *responseToolRegistry) addAlias(name string, binding *responseToolBinding) {
	key := toolNameKey(name)
	if key == "" || binding == nil {
		return
	}
	if existing, ok := r.aliases[key]; ok && existing != binding {
		// Do not make an ambiguous alias executable. Exact qualified names
		// remain available and suffix matching below only accepts unique hits.
		r.aliases[key] = nil
		return
	}
	r.aliases[key] = binding
}

func (r *responseToolRegistry) bindingForName(name string) *responseToolBinding {
	if r == nil {
		return nil
	}
	if binding, ok := r.aliases[toolNameKey(name)]; ok {
		return binding
	}
	candidate := strings.TrimPrefix(toolNameKey(name), "mcp__")
	var match *responseToolBinding
	for _, binding := range r.bindings {
		for _, known := range []string{binding.ClientName, binding.UpstreamName, binding.QualifiedName} {
			knownKey := toolNameKey(known)
			if candidate == knownKey || strings.HasSuffix(candidate, "__"+knownKey) {
				if match != nil && match != binding {
					return nil
				}
				match = binding
			}
		}
	}
	return match
}

// ClientNameFor converts a name emitted by the upstream model into the name
// that the Codex runtime registered.
func (r *responseToolRegistry) ClientNameFor(name string) (*responseToolBinding, bool) {
	binding := r.bindingForName(name)
	if binding == nil {
		return nil, false
	}
	return binding, true
}

// UpstreamNameFor converts a client-side Responses input item to the flat name
// used in the Chat Completions request sent upstream.
func (r *responseToolRegistry) UpstreamNameFor(name string) (string, *responseToolBinding) {
	if binding := r.bindingForName(name); binding != nil {
		return binding.UpstreamName, binding
	}
	return name, nil
}

func toolNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func normalizeResponseToolCall(tc *AggregatedToolCall, registry *responseToolRegistry) *responseToolBinding {
	if tc == nil || registry == nil {
		return nil
	}
	binding, ok := registry.ClientNameFor(tc.Name)
	if !ok {
		return nil
	}
	tc.Name = binding.ClientName
	if binding.Kind == responseToolCustom {
		tc.Type = responseToolCustom
	}
	return binding
}

func unwrapCustomResponseArgs(args string, binding *responseToolBinding) string {
	if binding == nil || binding.InputField == "" {
		return unwrapFreeformArgs(args)
	}
	return unwrapJSONField(args, binding.InputField)
}

func unwrapJSONField(args, field string) string {
	s := strings.TrimSpace(args)
	if s == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return args
	}
	value, ok := obj[field]
	if !ok {
		return args
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return args
	}
	return string(encoded)
}
