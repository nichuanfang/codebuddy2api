package service

import (
	"codebuddy-gateway/global"
	"codebuddy-gateway/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"io"
	"net/http"
	"strings"
	"time"
)

const statusClientClosedRequest = 499

var errRequestBodyTooLarge = errors.New("request body exceeds 32 MiB")

type Proxy struct {
	client    *UpstreamClient
	rotator   *Rotator
	refresher *Refresher
	history   *responseHistoryStore
}

func NewProxy(client *UpstreamClient, rotator *Rotator, refresher *Refresher) *Proxy {
	return &Proxy{client: client, rotator: rotator, refresher: refresher, history: newResponseHistoryStore()}
}

type ChatRequestMeta struct {
	Protocol       Protocol
	RequestedModel string
	UpstreamModel  string
	ClientStream   bool
	Body           []byte
	ClientIP       string
	RequestID      string
	UserAgent      string

	AffinityKey  string
	Compact      bool
	ToolRegistry *responseToolRegistry
	historyScope responseHistoryScope
	// ResponsesDowngradedTools records hosted/unknown Responses tools omitted
	// before forwarding to the Chat Completions upstream.
	ResponsesDowngradedTools []string
	// ResponsesDowngradedItems records hosted history items converted or skipped.
	ResponsesDowngradedItems []string
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
	normalizeChatTools(body)
	if global.CORE_LOG.Core().Enabled(zap.DebugLevel) {
		if rawTools, err := json.Marshal(body["tools"]); err == nil {
			global.CORE_LOG.Debug("chat tools sent upstream", zap.ByteString("tools", rawTools))
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
	scope := historyScopeFor(c.Request.Header, c.Query("api_key"), raw)
	if p.history != nil {
		raw, err = p.history.enrich(raw, scope)
		if err != nil {
			gatewayErrorWithDetails(c, ProtocolResponses, http.StatusBadRequest, err.Error(), "invalid_request_error", "input", requestIDFromContext(c))
			return
		}
	}
	meta, err := PrepareResponsesBody(raw)
	if err != nil {
		logResponsesPrepareError(c, raw, err)
		gatewayErrorWithDetails(c, ProtocolResponses, http.StatusBadRequest, err.Error(), "invalid_request_error", "", requestIDFromContext(c))
		return
	}
	attachClientMeta(c, meta)
	meta.historyScope = scope
	logResponsesDowngrade(meta, c)
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
		logResponsesPrepareError(c, raw, err)
		gatewayErrorWithDetails(c, ProtocolResponses, http.StatusBadRequest, err.Error(), "invalid_request_error", "", requestIDFromContext(c))
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
	global.CORE_LOG.Debug("relay request accepted",
		zap.String("protocol", string(meta.Protocol)),
		zap.String("model", meta.RequestedModel),
		zap.String("upstream_model", meta.UpstreamModel),
		zap.Bool("stream", meta.ClientStream),
		zap.Int("body_bytes", len(meta.Body)),
		zap.Int("retries", retries),
		zap.String("request_id", requestIDFromContext(c)),
		zap.String("client_ip", meta.ClientIP),
	)
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
		global.CORE_LOG.Debug("relay attempt started",
			zap.Int("attempt", i+1),
			zap.Uint("account_id", acc.ID),
			zap.String("upstream_model", meta.UpstreamModel),
		)
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
				p.observeUsage(acc, meta, start, statusClientClosedRequest, lastErr, nil)
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
					p.observeUsage(acc, meta, start, statusClientClosedRequest, lastErr, nil)
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
			lastUpstreamRequestID = upstreamRequestID(resp)
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
			if global.CORE_LOG.Core().Enabled(zap.DebugLevel) {
				global.CORE_LOG.Debug("upstream error body", zap.Int("upstream_status", resp.StatusCode), zap.String("error_body", string(clip(raw, 500))))
			}
			lastUpstreamStatus = resp.StatusCode
			lastUpstreamRaw = append(lastUpstreamRaw[:0], raw...)
			lastUpstreamRequestID = upstreamRequestID(resp)
			resp.Body.Close()
			cancel()
			lastErr = fmt.Sprintf("upstream %d: %s", resp.StatusCode, clip(raw, 200))
			if isUpstreamModelUnavailable(raw) && maybeRewriteUnavailableModel(meta) {
				global.CORE_LOG.Warn("upstream model unavailable, retrying same account with fallback",
					zap.Uint("account_id", acc.ID),
					zap.Int("upstream_status", resp.StatusCode),
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
				lastUpstreamRequestID = upstreamRequestID(resp)
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
					zap.Uint("account_id", acc.ID), zap.Int("upstream_status", resp.StatusCode))
				if retryWAFRejectedBody(meta) {
					delete(exclude, acc.ID)
					i--
					continue
				}
				global.CORE_LOG.Warn("escalating sanitize produced no change, giving up", zap.Uint("account_id", acc.ID))
			}
			if isUpstreamRequestError(raw) {
				global.CORE_LOG.Warn("upstream rejected request without burning account", zap.Uint("account_id", acc.ID), zap.Int("upstream_status", resp.StatusCode))
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
			gatewayErrorFromUpstream(c, meta.Protocol, status, lastErr, raw, upstreamRequestID(resp))
			p.observeUsage(acc, meta, start, resp.StatusCode, lastErr, nil)
			return
		}
		p.commitSuccess(c, acc, resp, cancel, meta, start)
		return
	}
	if lastErr == "" {
		lastErr = "no available codebuddy account"
	}
	global.CORE_LOG.Error("relay request failed",
		zap.String("protocol", string(meta.Protocol)),
		zap.String("model", meta.RequestedModel),
		zap.String("upstream_model", meta.UpstreamModel),
		zap.Int("upstream_status", lastUpstreamStatus),
		zap.Duration("latency", time.Since(start)),
		zap.String("request_id", requestIDFromContext(c)),
	)
	if lastUpstreamStatus != 0 && len(lastUpstreamRaw) > 0 {
		gatewayErrorFromUpstream(c, meta.Protocol, upstreamGatewayStatus(lastUpstreamStatus, lastUpstreamRaw), lastErr, lastUpstreamRaw, lastUpstreamRequestID)
		return
	}
	gatewayError(c, meta.Protocol, http.StatusServiceUnavailable, lastErr)
}
func upstreamRequestID(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	if id := resp.Header.Get("X-Request-Id"); id != "" {
		return id
	}
	return resp.Header.Get("X-Request-ID")
}
func propagateUpstreamRequestID(c *gin.Context, resp *http.Response) string {
	id := upstreamRequestID(resp)
	if c != nil && id != "" {
		c.Header("X-Request-Id", id)
	}
	return id
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
			// 断流/idle 超时通常是网络路径问题，不是账号的错。
			// 硬惩罚会把健康账号误打进冷却，这里只做软记录。
			if errors.Is(writeErr, errStreamIdle) {
				p.rotator.MarkTransientFailure(acc, errMsg)
			} else {
				p.rotator.MarkFailure(acc, errMsg)
			}
		default:
			status = http.StatusInternalServerError
		}
		global.CORE_LOG.Warn("write upstream response failed", zap.Int("status", status), zap.Error(writeErr))
		if !c.Writer.Written() {
			writeRelayFailure(c, meta.Protocol, status, writeErr)
		}
	} else {
		p.rotator.MarkSuccessFor(acc, meta.UpstreamModel)
	}
	p.observeUsage(acc, meta, start, status, errMsg, usage)
}
func writeRelayFailure(c *gin.Context, proto Protocol, status int, err error) {
	var upstreamErr *upstreamSSEError
	if proto == ProtocolResponses && errors.As(err, &upstreamErr) {
		gatewayErrorWithDetails(c, proto, status, upstreamErr.message, upstreamErr.code, "", "")
		return
	}
	gatewayError(c, proto, status, err.Error())
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
	reqID := propagateUpstreamRequestID(c, resp)
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
	watchdog := startStreamIdleWatchdog(c.Request.Context(), cancel, global.CORE_CONFIG.Gateway.StreamIdleTimeout())
	defer watchdog.stop()
	stopHeartbeat, heartbeatDead := startSSEHeartbeat(sink)
	defer stopHeartbeat()
	go func() {
		select {
		case <-heartbeatDead:
			cancel()
		case <-c.Request.Context().Done():
		}
	}()
	for {
		event, err := decoder.next()
		if err == io.EOF {
			if !normalSSETermination(false, finishReason) {
				failure := wrapUpstreamStream(errSSEMissingEnd)
				return usageWithStreamMeta(usage, firstTokenMs, reqID), failChatStream(sink, failure)
			}
			if !doneSeen {
				if _, doneErr := io.WriteString(sink, "data: [DONE]\n\n"); doneErr != nil {
					return usageWithStreamMeta(usage, firstTokenMs, reqID), wrapDownstreamWrite(doneErr)
				}
			}
			break
		}
		if err != nil {
			failure := streamReadError(err, watchdog, c.Request.Context())
			return usageWithStreamMeta(usage, firstTokenMs, reqID), failChatStream(sink, failure)
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
		usage = mergeUsage(usage, parsed)
		if _, err := io.WriteString(sink, out); err != nil {
			if usage != nil {
				usage.FirstTokenMs = firstTokenMs
			}
			return usageWithStreamMeta(usage, firstTokenMs, reqID), wrapDownstreamWrite(err)
		}
	}
	return usageWithStreamMeta(usage, firstTokenMs, reqID), nil
}
func (p *Proxy) writeJSON(c *gin.Context, resp *http.Response, meta *ChatRequestMeta, start time.Time, cancel context.CancelFunc) (*parsedUsage, error) {
	requestID := propagateUpstreamRequestID(c, resp)
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
		result.Usage.RequestID = requestID
	}
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
func usageWithStreamMeta(usage *parsedUsage, firstTokenMs int64, reqID string) *parsedUsage {
	if usage == nil {
		usage = &parsedUsage{}
	}
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
	meta.RequestID = requestIDFromContext(c)
	meta.UserAgent = clipText(c.Request.UserAgent(), 240)
}
func logResponsesPrepareError(c *gin.Context, raw []byte, err error) {
	if global.CORE_LOG == nil {
		return
	}
	global.CORE_LOG.Error("Responses request rejected",
		zap.String("model", requestModelFromRaw(raw)),
		zap.String("request_id", requestIDFromContext(c)),
		zap.Error(err),
	)
}
func logResponsesDowngrade(meta *ChatRequestMeta, c *gin.Context) {
	if meta == nil || global.CORE_LOG == nil {
		return
	}
	if len(meta.ResponsesDowngradedTools) == 0 && len(meta.ResponsesDowngradedItems) == 0 {
		return
	}
	// Codex can advertise web_search even when its own config disables it. The
	// harmless, repeated downgrade is high-volume and drowns real failures, so
	// keep only hosted-history or non-web-search downgrades at warn level.
	level := zap.WarnLevel
	if len(meta.ResponsesDowngradedItems) == 0 && onlyWebSearchDowngrade(meta.ResponsesDowngradedTools) {
		level = zap.DebugLevel
	}
	if ce := global.CORE_LOG.Check(level, "Responses request downgraded unsupported capabilities"); ce != nil {
		ce.Write(
			zap.String("model", meta.RequestedModel),
			zap.Strings("tools", meta.ResponsesDowngradedTools),
			zap.Strings("items", meta.ResponsesDowngradedItems),
			zap.String("request_id", requestIDFromContext(c)),
		)
	}
}

func onlyWebSearchDowngrade(tools []string) bool {
	for _, tool := range tools {
		if tool != "web_search" {
			return false
		}
	}
	return len(tools) > 0
}
func requestModelFromRaw(raw []byte) string {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	return asString(body["model"])
}
func requestIDFromContext(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if value := c.GetHeader("X-Request-Id"); value != "" {
		return value
	}
	return c.GetHeader("X-Request-ID")
}
func (p *Proxy) observeUsage(acc *model.Account, meta *ChatRequestMeta, start time.Time, status int, errMsg string, usage *parsedUsage) {
	if usage == nil {
		usage = &parsedUsage{}
	}
	if status == http.StatusOK {
		rememberGoodModel(meta.UpstreamModel)
	}
	deriveMetrics(usage, time.Since(start).Milliseconds())
	if usage.Credit > 0 {
		deduct, err := model.ApplyCreditDeduction(acc.ID, usage.Credit)
		if err == nil && deduct != nil {
			if deduct.Remain <= 0 && acc.CreditSyncedAt != nil {
				until := time.Now().Add(time.Duration(global.CORE_CONFIG.Watchdog.Cooldown()) * time.Second)
				_ = model.MarkAccountFailure(acc.ID, "credit exhausted", acc.FailCount+1, model.AccountStatusCooldown, &until)
			}
		}
	}
	if usage.TotalTokens > 0 {
		NoteModelCost(acc.ID, meta.UpstreamModel, usage.Credit, usage.TotalTokens)
	}

	fields := []zap.Field{
		zap.String("protocol", string(meta.Protocol)),
		zap.String("model", meta.RequestedModel),
		zap.String("upstream_model", meta.UpstreamModel),
		zap.Uint("account_id", acc.ID),
		zap.Int("status", status),
		zap.Int("prompt_tokens", usage.PromptTokens),
		zap.Int("completion_tokens", usage.CompletionTokens),
		zap.Int("total_tokens", usage.TotalTokens),
		zap.Float64("credit", usage.Credit),
		zap.Int64("latency_ms", usage.LatencyMs),
		zap.String("request_id", meta.RequestID),
		zap.String("upstream_request_id", usage.RequestID),
	}
	if status == http.StatusOK {
		global.CORE_LOG.Info("relay request completed", fields...)
	} else {
		global.CORE_LOG.Warn("relay request finished with error", fields...)
	}
}
func openaiError(c *gin.Context, status int, msg string) {
	openaiErrorWithDetails(c, status, msg, "codebuddy_gateway_error", "", "")
}
