package service

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxSSEEventBytes = 32 * 1024 * 1024

var (
	errSSEEventTooLarge = errors.New("upstream SSE event exceeds 32 MiB")
	errSSEDataMalformed = errors.New("upstream SSE data is not valid JSON")
	errSSEMissingEnd    = errors.New("upstream SSE stream ended before a normal termination")
)

type sseEvent struct {
	Event        string
	Data         string
	Chunk        map[string]any
	Done         bool
	FinishReason string
}

type sseDecoder struct {
	r         *bufio.Reader
	maxBytes  int
	eventSize int
	data      []string
	pending   string
	eventName string
}

func newSSEDecoder(r io.Reader) *sseDecoder {
	return &sseDecoder{r: bufio.NewReaderSize(r, 64*1024), maxBytes: maxSSEEventBytes}
}

// next returns one complete SSE event. A final event without a trailing blank
// line is accepted; the caller decides whether the stream itself terminated
// normally based on Done or FinishReason.
func (d *sseDecoder) next() (*sseEvent, error) {
	if d == nil || d.r == nil {
		return nil, io.EOF
	}
	for {
		var line string
		var err error
		if d.pending != "" {
			line = d.pending
			d.pending = ""
		} else {
			line, err = d.r.ReadString('\n')
		}
		if len(line) > 0 {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			// A few upstream test/proxy implementations omit the blank line
			// between one-line data events. If the accumulated data is already
			// a complete JSON value, dispatch it before retaining this line.
			if (strings.HasPrefix(line, "data:") || strings.HasPrefix(line, "event:")) && len(d.data) > 0 &&
				sseDataComplete(strings.Join(d.data, "\n")) {
				ev, parseErr := d.finishEvent()
				if parseErr != nil {
					return nil, parseErr
				}
				d.pending = line
				return ev, nil
			}
			d.eventSize += len(line)
			if d.eventSize > d.maxBytes {
				return nil, errSSEEventTooLarge
			}
			if line == "" {
				if len(d.data) > 0 {
					ev, parseErr := d.finishEvent()
					if parseErr != nil {
						return nil, parseErr
					}
					return ev, nil
				}
				d.eventSize = 0
				d.eventName = ""
			} else if strings.HasPrefix(line, "data:") {
				value := strings.TrimPrefix(line, "data:")
				if strings.HasPrefix(value, " ") {
					value = value[1:]
				}
				d.data = append(d.data, value)
			} else if strings.HasPrefix(line, "event:") {
				d.eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
		}
		if err != nil {
			if err == io.EOF && len(d.data) > 0 {
				ev, parseErr := d.finishEvent()
				if parseErr != nil {
					return nil, parseErr
				}
				return ev, nil
			}
			return nil, err
		}
	}
}

func sseDataComplete(data string) bool {
	if strings.TrimSpace(data) == "[DONE]" {
		return true
	}
	var chunk map[string]any
	return json.Unmarshal([]byte(data), &chunk) == nil
}

func (d *sseDecoder) finishEvent() (*sseEvent, error) {
	data := strings.Join(d.data, "\n")
	eventName := d.eventName
	d.data = nil
	d.eventName = ""
	d.eventSize = 0
	if strings.TrimSpace(data) == "[DONE]" {
		return &sseEvent{Event: eventName, Data: data, Done: true}, nil
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		if eventName == "error" {
			return &sseEvent{Event: eventName, Data: data}, nil
		}
		return nil, fmt.Errorf("%w: %v", errSSEDataMalformed, err)
	}
	return &sseEvent{
		Event:        eventName,
		Data:         data,
		Chunk:        chunk,
		FinishReason: chunkFinishReason(chunk),
	}, nil
}

type upstreamSSEError struct {
	code    string
	message string
}

func (e *upstreamSSEError) Error() string { return e.message }

func (e *sseEvent) upstreamError() error {
	if e == nil || (e.Event != "error" && (e.Chunk == nil || e.Chunk["error"] == nil)) {
		return nil
	}
	info := parseUpstreamError([]byte(e.Data))
	message := info.Message
	if message == "" {
		if text, ok := e.Chunk["error"].(string); ok {
			message = text
		}
	}
	if message == "" && e.Event == "error" && e.Chunk == nil {
		message = e.Data
	}
	if strings.TrimSpace(message) == "" {
		message = "upstream reported an error"
	}
	code := info.Code
	if code == "" {
		code = "upstream_error"
	}
	return &upstreamSSEError{code: clipText(code, 80), message: clipText(strings.TrimSpace(message), 500)}
}

func chunkFinishReason(chunk map[string]any) string {
	if chunk == nil {
		return ""
	}
	choices, _ := chunk["choices"].([]any)
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		if choice == nil {
			continue
		}
		if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
			return reason
		}
	}
	return ""
}

func chunkHasGeneratedToken(chunk map[string]any) bool {
	if chunk == nil {
		return false
	}
	choices, _ := chunk["choices"].([]any)
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		if choice == nil {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			if msg, ok := choice["message"].(map[string]any); ok {
				delta = msg
			}
		}
		if delta == nil {
			continue
		}
		if deltaString(delta["content"]) != "" || deltaString(delta["reasoning_content"]) != "" {
			return true
		}
		if tcs, ok := delta["tool_calls"].([]any); ok && len(tcs) > 0 {
			return true
		}
	}
	return false
}

func (e *sseEvent) normalTermination() bool {
	return e != nil && (e.Done || e.FinishReason != "")
}
