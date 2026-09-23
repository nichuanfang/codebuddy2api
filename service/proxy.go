package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"codebuddy-gateway/global"
	"codebuddy-gateway/model"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const statusClientClosedRequest = 499

var errRequestBodyTooLarge = errors.New("request body exceeds 32 MiB")

type Proxy struct {
	client    *UpstreamClient
	rotator   *Rotator
	refresher *Refresher
}

func NewProxy(client *UpstreamClient, rotator *Rotator, refresher *Refresher) *Proxy {
	return &Proxy{client: client, rotator: rotator, refresher: refresher}
}

type ChatRequestMeta struct {
	Protocol       Protocol
	RequestedModel string
	UpstreamModel  string
	ClientStream   bool
	Body           []byte
	ClientIP       string
	UserAgent      string
	RequestPreview string
	AffinityKey    string
	Compact        bool
}

func PrepareChatBody(raw []byte) (*ChatRequestMeta, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("invalid json body")
	}
	requested, _ := body["model"].(string)
	if requested == "" {
		return nil, fmt.Errorf("model is required")
	}
	upstream := ResolveModelAlias(requested)
	body["model"] = upstream
	clientStream := true
	if v, ok := body["stream"].(bool); ok {
		clientStream = v
	}
	body["stream"] = true
	if global.CORE_CONFIG.Gateway.InjectReasoning {
		if _, ok := body["reasoningEffort"]; !ok {
			body["reasoningEffort"] = "medium"
		}
		if _, ok := body["reasoning_summary"]; !ok {
			body["reasoning_summary"] = "auto"
		}
	}
	sanitizeUpstreamChatWithMode(body, global.CORE_CONFIG.Gateway.SanitizeModeName())
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &ChatRequestMeta{
		Protocol:       ProtocolChat,
		RequestedModel: requested,
		UpstreamModel:  upstream,
		ClientStream:   clientStream,
		Body:           encoded,
		RequestPreview: captureChatRequest(encoded),
	}, nil
}

func (p *Proxy) HandleChat(c *gin.Context) {
	raw, err := readRequestBody(c)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errRequestBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		gatewayError(c, ProtocolChat, status, err.Error())
		return
	}
	meta, err := PrepareChatBody(raw)
	if err != nil {
		gatewayError(c, ProtocolChat, http.StatusBadRequest, err.Error())
		return
	}
	attachClientMeta(c, meta)
	meta.AffinityKey = RequestAffinityKey(c.Request.Header, meta.Body)
	p.relay(c, meta, "/v2/chat/completions")
}

func (p *Proxy) HandleResponses(c *gin.Context) {
	raw, err := readRequestBody(c)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errRequestBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		gatewayError(c, ProtocolResponses, status, err.Error())
		return
	}
	meta, err := PrepareResponsesBody(raw)
	if err != nil {
		gatewayError(c, ProtocolResponses, http.StatusBadRequest, err.Error())
		return
	}
	attachClientMeta(c, meta)
	meta.AffinityKey = RequestAffinityKey(c.Request.Header, meta.Body)
	p.relay(c, meta, "/v2/chat/completions")
}

func (p *Proxy) HandleResponsesCompact(c *gin.Context) {
	raw, err := readRequestBody(c)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errRequestBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		gatewayError(c, ProtocolResponses, status, err.Error())
		return
	}
	meta, err := PrepareResponsesCompactBody(raw)
	if err != nil {
		gatewayError(c, ProtocolResponses, http.StatusBadRequest, err.Error())
		return
	}
	attachClientMeta(c, meta)
	meta.AffinityKey = RequestAffinityKey(c.Request.Header, meta.Body)
	p.relay(c, meta, "/v2/chat/completions")
}

func (p *Proxy) HandleMessages(c *gin.Context) {
	raw, err := readRequestBody(c)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errRequestBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		gatewayError(c, ProtocolAnthropic, status, err.Error())
		return
	}
	meta, err := PrepareAnthropicBody(raw)
	if err != nil {
		gatewayError(c, ProtocolAnthropic, http.StatusBadRequest, err.Error())
		return
	}
	attachClientMeta(c, meta)
	meta.AffinityKey = RequestAffinityKey(c.Request.Header, meta.Body)
	p.relay(c, meta, "/v2/chat/completions")
}

func (p *Proxy) HandleCountTokens(c *gin.Context) {
	raw, err := readRequestBody(c)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errRequestBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		gatewayError(c, ProtocolAnthropic, status, err.Error())
		return
	}
	c.Header("anthropic-version", "2023-06-01")
	if global.CORE_CONFIG.Gateway.CountTokensModeName() == "upstream" {
		if _, err := PrepareAnthropicBody(raw); err != nil {
			gatewayError(c, ProtocolAnthropic, http.StatusBadRequest, err.Error())
			return
		}
		tokens, err := p.countTokensUpstream(c.Request.Context(), c.Request.Header, raw)
		if err != nil {
			gatewayError(c, ProtocolAnthropic, http.StatusBadGateway, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"input_tokens": tokens})
		return
	}
	c.JSON(http.StatusOK, gin.H{"input_tokens": estimateTokenCount(raw)})
}

func (p *Proxy) countTokensUpstream(ctx context.Context, headers http.Header, raw []byte) (int, error) {
	meta, err := PrepareAnthropicBody(raw)
	if err != nil {
		return 0, err
	}
	var body map[string]any
	if err := json.Unmarshal(meta.Body, &body); err != nil {
		return 0, fmt.Errorf("count_tokens body conversion failed: %w", err)
	}
	body["max_tokens"] = 1
	body["stream"] = true
	body["stream_options"] = map[string]any{"include_usage": true}
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	meta.Body = encoded
	meta.AffinityKey = RequestAffinityKey(headers, encoded)

	exclude := map[uint]struct{}{}
	refreshed := map[uint]bool{}
	var lastErr error
	for attempt := 0; attempt < global.CORE_CONFIG.Gateway.Retries(); attempt++ {
		acc, err := p.rotator.NextForAffinity(exclude, meta.UpstreamModel, meta.AffinityKey)
		if err != nil {
			lastErr = err
			break
		}
		exclude[acc.ID] = struct{}{}
		if global.CORE_CONFIG.Refresh.Enabled && ShouldRefresh(acc.JWT, time.Hour) {
			if refreshErr := p.refresher.RefreshAccount(ctx, acc); refreshErr != nil {
				lastErr = refreshErr
				p.rotator.MarkFailure(acc, refreshErr.Error())
				continue
			}
		}

		resp, cancel, err := p.doUpstreamAttempt(ctx, acc, "/v2/chat/completions", encoded)
		if err != nil {
			lastErr = err
			if downstreamRequestCanceled(ctx, err) {
				return 0, err
			}
			p.rotator.MarkFailure(acc, err.Error())
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			cancel()
			if !refreshed[acc.ID] && global.CORE_CONFIG.Refresh.Enabled {
				refreshed[acc.ID] = true
				if refreshErr := p.refresher.RefreshAccount(ctx, acc); refreshErr == nil {
					delete(exclude, acc.ID)
					attempt--
					continue
				} else {
					lastErr = refreshErr
				}
			} else {
				lastErr = fmt.Errorf("upstream returned unauthorized after token refresh")
			}
			p.rotator.MarkFailure(acc, lastErr.Error())
			continue
		}
		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			cancel()
			lastErr = fmt.Errorf("upstream count_tokens http %d: %s", resp.StatusCode, clip(errBody, 300))
			if isModelQuotaExhausted(resp.StatusCode, errBody) {
				p.rotator.MarkModelExhausted(acc, meta.UpstreamModel, lastErr.Error())
			} else {
				p.rotator.MarkFailure(acc, lastErr.Error())
			}
			continue
		}

		result, collectErr := collectSSE(resp.Body, meta.UpstreamModel)
		resp.Body.Close()
		cancel()
		if collectErr != nil {
			lastErr = collectErr
			p.rotator.MarkFailure(acc, collectErr.Error())
			continue
		}
		if result == nil || result.Usage == nil || result.Usage.PromptTokens <= 0 {
			lastErr = fmt.Errorf("upstream count_tokens response did not include input usage")
			p.rotator.MarkFailure(acc, lastErr.Error())
			continue
		}
		p.rotator.MarkSuccessFor(acc, meta.UpstreamModel)
		return result.Usage.PromptTokens, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no available codebuddy account for count_tokens")
	}
	return 0, lastErr
}

func (p *Proxy) HandleCompletions(c *gin.Context) {
	raw, err := readRequestBody(c)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errRequestBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		openaiError(c, status, err.Error())
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		openaiError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	requested, _ := body["model"].(string)
	if requested == "" {
		requested = "codewise-completions"
	}
	upstream := ResolveModelAlias(requested)
	if upstream == requested {
		upstream = "codewise-completions"
	}
	body["model"] = upstream
	clientStream := true
	if v, ok := body["stream"].(bool); ok {
		clientStream = v
	}
	body["stream"] = true
	encoded, err := json.Marshal(body)
	if err != nil {
		openaiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	meta := &ChatRequestMeta{
		Protocol:       ProtocolChat,
		RequestedModel: requested,
		UpstreamModel:  upstream,
		ClientStream:   clientStream,
		Body:           encoded,
	}
	attachClientMeta(c, meta)
	meta.AffinityKey = RequestAffinityKey(c.Request.Header, meta.Body)
	p.relay(c, meta, "/v2/completions")
}

func (p *Proxy) relay(c *gin.Context, meta *ChatRequestMeta, path string) {
	start := time.Now()
	exclude := map[uint]struct{}{}
	retries := global.CORE_CONFIG.Gateway.Retries()
	var lastErr string
	var lastUpstreamStatus int
	var lastUpstreamRaw []byte
	var lastUpstreamRequestID string
	wafRetried := false

	for i := 0; i < retries; i++ {
		acc, err := p.rotator.NextForAffinity(exclude, meta.UpstreamModel, meta.AffinityKey)
		if err != nil {
			lastErr = err.Error()
			break
		}
		exclude[acc.ID] = struct{}{}

		if ShouldRefresh(acc.JWT, time.Hour) {
			if refreshErr := p.refresher.RefreshAccount(c.Request.Context(), acc); refreshErr != nil {
				global.CORE_LOG.Warn("preemptive refresh failed", zap.Uint("account_id", acc.ID), zap.Error(refreshErr))
			}
		}

		resp, cancel, err := p.doUpstreamAttempt(c.Request.Context(), acc, path, meta.Body)
		if err != nil {
			lastErr = err.Error()
			if downstreamRequestCanceled(c.Request.Context(), err) {
				c.Status(statusClientClosedRequest)
				p.recordUsage(acc, meta, start, statusClientClosedRequest, lastErr, nil)
				return
			}
			p.rotator.MarkFailure(acc, lastErr)
			continue
		}

		if resp.StatusCode == http.StatusUnauthorized {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			cancel()
			if refreshErr := p.refresher.RefreshAccount(c.Request.Context(), acc); refreshErr != nil {
				lastErr = "jwt invalid and refresh failed: " + refreshErr.Error()
				p.rotator.MarkFailure(acc, lastErr)
				continue
			}
			resp, cancel, err = p.doUpstreamAttempt(c.Request.Context(), acc, path, meta.Body)
			if err != nil {
				lastErr = err.Error()
				if downstreamRequestCanceled(c.Request.Context(), err) {
					c.Status(statusClientClosedRequest)
					p.recordUsage(acc, meta, start, statusClientClosedRequest, lastErr, nil)
					return
				}
				p.rotator.MarkFailure(acc, lastErr)
				continue
			}
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
			raw, _ := io.ReadAll(resp.Body)
			lastUpstreamStatus = resp.StatusCode
			lastUpstreamRaw = append(lastUpstreamRaw[:0], raw...)
			lastUpstreamRequestID = resp.Header.Get("X-Request-Id")
			resp.Body.Close()
			cancel()
			lastErr = fmt.Sprintf("upstream %d: %s", resp.StatusCode, clip(raw, 200))
			if isModelQuotaExhausted(resp.StatusCode, raw) {
				p.rotator.MarkModelExhausted(acc, meta.UpstreamModel, lastErr)
			} else {
				p.rotator.MarkFailure(acc, lastErr)
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			lastUpstreamStatus = resp.StatusCode
			lastUpstreamRaw = append(lastUpstreamRaw[:0], raw...)
			lastUpstreamRequestID = resp.Header.Get("X-Request-Id")
			resp.Body.Close()
			cancel()
			lastErr = fmt.Sprintf("upstream %d: %s", resp.StatusCode, clip(raw, 200))
			if isUpstreamModelUnavailable(raw) && maybeRewriteUnavailableModel(meta) {
				global.CORE_LOG.Warn("upstream model unavailable, retrying same account with fallback",
					zap.Uint("account_id", acc.ID),
					zap.String("error", lastErr),
					zap.String("fallback", meta.UpstreamModel))
				resp, cancel, err = p.doUpstreamAttempt(c.Request.Context(), acc, path, meta.Body)
				if err != nil {
					lastErr = err.Error()
					continue
				}
				if resp.StatusCode == http.StatusOK {
					p.commitSuccess(c, acc, resp, cancel, meta, start)
					return
				}
				raw, _ = io.ReadAll(resp.Body)
				lastUpstreamStatus = resp.StatusCode
				lastUpstreamRaw = append(lastUpstreamRaw[:0], raw...)
				lastUpstreamRequestID = resp.Header.Get("X-Request-Id")
				resp.Body.Close()
				cancel()
				lastErr = fmt.Sprintf("upstream %d: %s", resp.StatusCode, clip(raw, 200))
			}
			if isModelQuotaExhausted(resp.StatusCode, raw) {
				p.rotator.MarkModelExhausted(acc, meta.UpstreamModel, lastErr)
				continue
			}
			if isUnapprovedChannel(raw) && !wafRetried {
				wafRetried = true
				global.CORE_LOG.Warn("upstream blocked by security policy, escalating sanitize and retrying",
					zap.Uint("account_id", acc.ID), zap.String("error", lastErr))
				if retryWAFRejectedBody(meta) {
					delete(exclude, acc.ID)
					i--
					continue
				}
				global.CORE_LOG.Warn("escalating sanitize produced no change, giving up", zap.Uint("account_id", acc.ID))
			}
			if isUpstreamRequestError(raw) {
				global.CORE_LOG.Warn("upstream rejected request without burning account", zap.Uint("account_id", acc.ID), zap.String("error", lastErr))
			} else {
				p.rotator.MarkFailure(acc, lastErr)
			}
			if resp.StatusCode >= 500 {
				continue
			}
			status := http.StatusBadGateway
			if isUpstreamRequestError(raw) {
				status = http.StatusBadRequest
			}
			gatewayErrorFromUpstream(c, meta.Protocol, status, lastErr, raw, resp.Header.Get("X-Request-Id"))
			p.recordUsage(acc, meta, start, resp.StatusCode, lastErr, nil)
			return
		}

		p.commitSuccess(c, acc, resp, cancel, meta, start)
		return
	}

	if lastErr == "" {
		lastErr = "no available codebuddy account"
	}
	if lastUpstreamStatus != 0 && len(lastUpstreamRaw) > 0 {
		gatewayErrorFromUpstream(c, meta.Protocol, upstreamGatewayStatus(lastUpstreamStatus, lastUpstreamRaw), lastErr, lastUpstreamRaw, lastUpstreamRequestID)
		return
	}
	gatewayError(c, meta.Protocol, http.StatusServiceUnavailable, lastErr)
}

func upstreamGatewayStatus(status int, raw []byte) int {
	if status >= 500 {
		return http.StatusBadGateway
	}
	if status == http.StatusBadRequest && !isUpstreamRequestError(raw) {
		return http.StatusBadGateway
	}
	return status
}

func (p *Proxy) commitSuccess(c *gin.Context, acc *model.Account, resp *http.Response, cancel context.CancelFunc, meta *ChatRequestMeta, start time.Time) {
	defer cancel()
	usage, writeErr := p.writeResponse(c, resp, meta, start, cancel)
	status := http.StatusOK
	errMsg := ""
	if writeErr != nil {
		errMsg = writeErr.Error()
		switch {
		case errors.Is(writeErr, errDownstreamWrite):
			status = statusClientClosedRequest
		case errors.Is(writeErr, errUpstreamStream):
			status = http.StatusBadGateway
			p.rotator.MarkFailure(acc, errMsg)
		default:
			status = http.StatusInternalServerError
		}
		global.CORE_LOG.Warn("write upstream response failed", zap.Int("status", status), zap.Error(writeErr))
		if !c.Writer.Written() {
			gatewayError(c, meta.Protocol, status, errMsg)
		}
	} else {
		p.rotator.MarkSuccessFor(acc, meta.UpstreamModel)
	}
	p.recordUsage(acc, meta, start, status, errMsg, usage)
}

func downstreamRequestCanceled(ctx context.Context, err error) bool {
	if ctx == nil || ctx.Err() == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (p *Proxy) doUpstreamAttempt(parent context.Context, acc *model.Account, path string, body []byte) (*http.Response, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(parent)
	resp, err := p.doUpstream(ctx, acc, path, body)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

func (p *Proxy) doUpstream(ctx context.Context, acc *model.Account, path string, body []byte) (*http.Response, error) {
	if path == "/v2/completions" {
		return p.client.Completions(ctx, acc, body)
	}
	return p.client.ChatCompletions(ctx, acc, body)
}

func (p *Proxy) writeResponse(c *gin.Context, resp *http.Response, meta *ChatRequestMeta, start time.Time, cancel context.CancelFunc) (*parsedUsage, error) {
	defer resp.Body.Close()
	if meta.Protocol == ProtocolResponses || meta.Protocol == ProtocolAnthropic {
		if meta.ClientStream {
			return p.writeCompatStream(c, resp, meta, start, cancel)
		}
		return p.writeCompatJSON(c, resp, meta, start, cancel)
	}
	if meta.ClientStream {
		return p.writeStream(c, resp, meta, start, cancel)
	}
	return p.writeJSON(c, resp, meta, start, cancel)
}

func (p *Proxy) writeStream(c *gin.Context, resp *http.Response, meta *ChatRequestMeta, start time.Time, cancel context.CancelFunc) (*parsedUsage, error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	sink := newSSESink(c.Writer, flusher)

	decoder := newSSEDecoder(resp.Body)
	var usage *parsedUsage
	var firstTokenMs int64
	finishReason := ""
	doneSeen := false
	collector := NewCaptureCollector()
	reqID := resp.Header.Get("X-Request-Id")
	watchdog := startStreamIdleWatchdog(c.Request.Context(), cancel, global.CORE_CONFIG.Gateway.StreamIdleTimeout())
	defer watchdog.stop()
	stopHeartbeat := startSSEHeartbeat(sink)
	defer stopHeartbeat()

	for {
		event, err := decoder.next()
		if err == io.EOF {
			if !normalSSETermination(false, finishReason) {
				failure := wrapUpstreamStream(errSSEMissingEnd)
				return usageWithStreamMeta(usage, collector, firstTokenMs, reqID), failChatStream(sink, failure)
			}
			if !doneSeen {
				if _, doneErr := io.WriteString(sink, "data: [DONE]\n\n"); doneErr != nil {
					return usageWithStreamMeta(usage, collector, firstTokenMs, reqID), wrapDownstreamWrite(doneErr)
				}
			}
			break
		}
		if err != nil {
			failure := streamReadError(err, watchdog, c.Request.Context())
			return usageWithStreamMeta(usage, collector, firstTokenMs, reqID), failChatStream(sink, failure)
		}
		watchdog.touch()
		if event.Done {
			doneSeen = true
			break
		}
		if event.FinishReason != "" {
			finishReason = event.FinishReason
		}
		if firstTokenMs == 0 && chunkHasGeneratedToken(event.Chunk) {
			firstTokenMs = time.Since(start).Milliseconds()
		}
		out, parsed := rewriteSSEEvent(event, meta.RequestedModel)
		if out == "" {
			continue
		}
		if parsed == nil {
			parsed = &parsedUsage{}
		}
		parsed.Collector = collector
		collector.Write(chunkTextFromChunk(event.Chunk))
		usage = mergeUsage(usage, parsed)
		if _, err := io.WriteString(sink, out); err != nil {
			if usage != nil {
				usage.FirstTokenMs = firstTokenMs
			}
			return usageWithStreamMeta(usage, collector, firstTokenMs, reqID), wrapDownstreamWrite(err)
		}
	}
	return usageWithStreamMeta(usage, collector, firstTokenMs, reqID), nil
}

func (p *Proxy) writeJSON(c *gin.Context, resp *http.Response, meta *ChatRequestMeta, start time.Time, cancel context.CancelFunc) (*parsedUsage, error) {
	watchdog := startStreamIdleWatchdog(c.Request.Context(), cancel, global.CORE_CONFIG.Gateway.StreamIdleTimeout())
	defer watchdog.stop()
	result, err := collectSSEWithStart(resp.Body, meta.RequestedModel, start)
	if err != nil {
		if watchdog != nil && watchdog.timedOutNow() {
			return nil, errors.Join(errUpstreamStream, errStreamIdle, err)
		}
		if c.Request.Context().Err() != nil {
			return nil, wrapDownstreamWrite(c.Request.Context().Err())
		}
		return nil, wrapUpstreamStream(err)
	}
	if result.Usage == nil {
		result.Usage = &parsedUsage{}
	}
	if result.Usage.RequestID == "" {
		result.Usage.RequestID = resp.Header.Get("X-Request-Id")
	}
	collector := NewCaptureCollector()
	collector.Write(jsonResponseText(result))
	result.Usage.Collector = collector
	agg, err := encodeChatJSON(result)
	if err != nil {
		return result.Usage, err
	}
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	if _, writeErr := c.Writer.Write(stripInvisibleFromFrame(agg)); writeErr != nil {
		return result.Usage, wrapDownstreamWrite(writeErr)
	}
	return result.Usage, nil
}

func failChatStream(sink io.Writer, err error) error {
	payload := map[string]any{"error": map[string]any{
		"message": err.Error(),
		"type":    "codebuddy_gateway_error",
	}}
	data, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return wrapUpstreamStream(marshalErr)
	}
	if _, writeErr := io.WriteString(sink, "data: "+string(data)+"\n\n"); writeErr != nil {
		return wrapDownstreamWrite(writeErr)
	}
	if _, writeErr := io.WriteString(sink, "data: [DONE]\n\n"); writeErr != nil {
		return wrapDownstreamWrite(writeErr)
	}
	return err
}

func usageWithStreamMeta(usage *parsedUsage, collector *CaptureCollector, firstTokenMs int64, reqID string) *parsedUsage {
	if usage == nil {
		usage = &parsedUsage{}
	}
	usage.Collector = collector
	usage.FirstTokenMs = firstTokenMs
	if reqID != "" && usage.RequestID == "" {
		usage.RequestID = reqID
	}
	return usage
}

func normalSSETermination(done bool, finishReason string) bool {
	return done || finishReason != ""
}

func rewriteSSEEvent(event *sseEvent, requestedModel string) (string, *parsedUsage) {
	if event == nil {
		return "", nil
	}
	if event.Done {
		return "data: [DONE]\n\n", nil
	}
	chunk := event.Chunk
	if chunk == nil {
		return "", nil
	}
	if requestedModel != "" {
		chunk["model"] = requestedModel
	}
	if !global.CORE_CONFIG.Gateway.Passthrough {
		stripNonOpenAI(chunk)
	}
	usage := extractUsage(chunk)
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return "", usage
	}
	return "data: " + string(encoded) + "\n\n", usage
}

func rewriteSSELine(line, requestedModel string) (string, *parsedUsage) {
	done, chunk := parseChatSSELine(line)
	event := &sseEvent{Done: done, Chunk: chunk}
	if chunk != nil {
		event.FinishReason = chunkFinishReason(chunk)
	}
	out, usage := rewriteSSEEvent(event, requestedModel)
	if out == "" {
		return line, usage
	}
	return strings.TrimSuffix(out, "\n"), usage
}

func aggregateSSE(r io.Reader, requestedModel string) ([]byte, *parsedUsage, error) {
	result, err := collectSSE(r, requestedModel)
	if err != nil {
		return nil, nil, err
	}
	encoded, err := encodeChatJSON(result)
	if err != nil {
		return nil, result.Usage, err
	}
	return encoded, result.Usage, nil
}

func stripNonOpenAI(chunk map[string]any) {
	if choices, ok := chunk["choices"].([]any); ok {
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			if choice == nil {
				continue
			}
			if delta, ok := choice["delta"].(map[string]any); ok {
				delete(delta, "reasoning_content")
				delete(delta, "refusal")
				delete(delta, "extra_fields")
				if fc, ok := delta["function_call"].(map[string]any); ok {
					name, _ := fc["name"].(string)
					args, _ := fc["arguments"].(string)
					if name == "" && args == "" {
						delete(delta, "function_call")
					}
				}
			}
		}
	}
	if usage, ok := chunk["usage"].(map[string]any); ok {
		delete(usage, "credit")
	}
}

const maxRequestBodyBytes = 32 << 20

func readRequestBody(c *gin.Context) ([]byte, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			return nil, errRequestBodyTooLarge
		}
		return nil, err
	}
	return raw, nil
}

func attachClientMeta(c *gin.Context, meta *ChatRequestMeta) {
	if meta == nil {
		return
	}
	meta.ClientIP = c.ClientIP()
	meta.UserAgent = clipText(c.Request.UserAgent(), 240)
}

func (p *Proxy) recordUsage(acc *model.Account, meta *ChatRequestMeta, start time.Time, status int, errMsg string, usage *parsedUsage) {
	if usage == nil {
		usage = &parsedUsage{}
	}
	if status == http.StatusOK {
		rememberGoodModel(meta.UpstreamModel)
	}
	deriveMetrics(usage, time.Since(start).Milliseconds())
	log := &model.UsageLog{
		AccountID:                acc.ID,
		AccountName:              accountLabel(acc),
		Protocol:                 string(meta.Protocol),
		Model:                    meta.RequestedModel,
		UpstreamModel:            meta.UpstreamModel,
		Stream:                   meta.ClientStream,
		PromptTokens:             usage.PromptTokens,
		CompletionTokens:         usage.CompletionTokens,
		TotalTokens:              usage.TotalTokens,
		ThinkingTokens:           usage.ThinkingTokens,
		Credit:                   usage.Credit,
		EstimatedCredit:          estimateCredit(usage.PromptTokens, usage.CompletionTokens),
		CacheHitTokens:           usage.CacheHitTokens,
		CacheMissTokens:          usage.CacheMissTokens,
		CacheHitRate:             usage.CacheHitRate,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheWriteTokens:         usage.CacheWriteTokens,
		CachedTokens:             usage.CachedTokens,
		FirstTokenMs:             usage.FirstTokenMs,
		LatencyMs:                usage.LatencyMs,
		TokensPerSecond:          usage.TokensPerSecond,
		OutputTokensPerSecond:    usage.OutputTokensPerSecond,
		StatusCode:               status,
		Error:                    errMsg,
		RequestID:                usage.RequestID,
		ClientIP:                 meta.ClientIP,
		UserAgent:                meta.UserAgent,
		RequestTokens:            len(meta.Body),
		RequestPreview:           meta.RequestPreview,
		ResponseTokens:           usage.ResponseBytes,
		ResponsePreview:          usage.ResponsePreview,
		RawUsage:                 clipRawUsage(usage.Raw),
	}
	if usage.Credit > 0 {
		if deduct, err := model.ApplyCreditDeduction(acc.ID, usage.Credit); err == nil && deduct != nil {
			log.CreditMonthly = deduct.Monthly
			log.CreditOnetime = deduct.Onetime
			log.CreditSource = deduct.Source
			if deduct.Remain <= 0 && acc.CreditSyncedAt != nil {
				until := time.Now().Add(time.Duration(global.CORE_CONFIG.Watchdog.Cooldown()) * time.Second)
				_ = model.MarkAccountFailure(acc.ID, "credit exhausted", acc.FailCount+1, model.AccountStatusCooldown, &until)
			}
		}
	}
	// 成本账本：记录该账号跑该模型的实测单价，供下次选号优先挑免费的。
	//
	// 只记上游真回了 usage 的请求：tokens 为 0 时可能是响应缺 usage 字段，
	// 那种情况记成「0 单价」会把未知误判成免费，让这个号垄断该模型流量。
	// 这一条同时覆盖免费观测（credit=0 且 tokens>0 才是真的免费）。
	if usage.TotalTokens > 0 {
		NoteModelCost(acc.ID, meta.UpstreamModel, usage.Credit, usage.TotalTokens)
	}
	_ = model.CreateUsageLog(log)
}

func openaiError(c *gin.Context, status int, msg string) {
	openaiErrorWithDetails(c, status, msg, "codebuddy_gateway_error", "", "")
}
