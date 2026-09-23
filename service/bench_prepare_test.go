package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"codebuddy-gateway/config"
	"codebuddy-gateway/global"
)

// 本文件固化 Requests 前置处理的性能基准，用于防止回归。
//
// 背景：Responses 请求在转发上游之前要走「Responses→Chat 转换 → 出站清洗 →
// 预览提取」三段。其中出站清洗（sanitize.go）曾是主要瓶颈，因为词表被编译成
// 单个大 alternation 正则，在长上下文上退化为逐位置扫描。改为 Aho-Corasick
// 自动机（sanitize_matcher.go）后，默认 harness 档从约 28ms 降到约 5.2ms。
//
// 运行方式：
//
//	go test ./service/ -run XXX -bench BenchmarkPrepare -benchtime 20x
//
// 若要观察清洗本身的成本，可对比 SanitizeOff 与默认档的差值。

// benchResponsesBody 构造一个贴近真实的 Codex Responses 请求体：
// 多轮对话历史 + 工具调用记录 + 大量工具定义 + 含敏感词的正文。
// 参数规模刻意偏大（真实 agentic 会话的长尾），以便暴露长上下文上的退化。
func benchResponsesBody(toolCount, turns int) []byte {
	input := make([]any, 0, turns*4)
	for i := 0; i < turns; i++ {
		input = append(input,
			map[string]any{
				"type": "message", "role": "user",
				"content": []any{map[string]any{
					"type": "input_text",
					"text": strings.Repeat("Please read the file and explain the architecture. ", 30),
				}},
			},
			map[string]any{
				"type": "function_call", "call_id": fmt.Sprintf("call_%d", i),
				"name": "shell", "arguments": `{"cmd":"grep -rn sandbox .","timeout":10000}`,
			},
			map[string]any{
				"type": "function_call_output", "call_id": fmt.Sprintf("call_%d", i),
				"output": strings.Repeat("line of output with credential testing and exploit discussion ", 20),
			},
			map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{
					"type": "output_text",
					"text": strings.Repeat("Here is the analysis of the sandbox and escalation path. ", 30),
				}},
			},
		)
	}
	tools := make([]any, 0, toolCount)
	for i := 0; i < toolCount; i++ {
		tools = append(tools, map[string]any{
			"type": "function", "name": fmt.Sprintf("tool_%d", i),
			"description": strings.Repeat("Run a sandboxed command with credential handling and escalation checks. ", 8),
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"cmd": map[string]any{"type": "string"}},
			},
		})
	}
	body := map[string]any{
		"model":  "glm-5.3",
		"stream": true,
		"instructions": strings.Repeat(
			"You are Codex CLI, an open source project led by OpenAI. Follow the sandbox rules. ", 20),
		"input":             input,
		"tools":             tools,
		"max_output_tokens": 8192,
	}
	raw, _ := json.Marshal(body)
	return raw
}

// BenchmarkPrepareResponses 是默认（harness）清洗档的端到端基线。
// 这是最常见的线上路径：Codex 发 Responses，网关转换 + 清洗后转发。
func BenchmarkPrepareResponses(b *testing.B) {
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true, SanitizeMode: "harness"}
	raw := benchResponsesBody(40, 12)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := PrepareResponsesBody(raw); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPrepareResponsesSanitizeOff 提供「不做清洗」的下界，
// 与 BenchmarkPrepareResponses 的差值即清洗本身的开销。
func BenchmarkPrepareResponsesSanitizeOff(b *testing.B) {
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true, SanitizeMode: "off"}
	raw := benchResponsesBody(40, 12)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := PrepareResponsesBody(raw); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPrepareResponsesFull 覆盖 full 档：它额外做结构性块重写与句子级剪枝。
func BenchmarkPrepareResponsesFull(b *testing.B) {
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true, SanitizeMode: "full"}
	raw := benchResponsesBody(40, 12)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := PrepareResponsesBody(raw); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPrepareResponsesLargeContext 用更长的会话（90 个工具、40 轮）压测，
// 用于确认成本随上下文长度保持线性、没有退化成超线性。
func BenchmarkPrepareResponsesLargeContext(b *testing.B) {
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true, SanitizeMode: "harness"}
	raw := benchResponsesBody(90, 40)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := PrepareResponsesBody(raw); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPrepareChat 覆盖 Chat Completions 入口，确认清洗优化同样惠及该路径。
func BenchmarkPrepareChat(b *testing.B) {
	global.CORE_CONFIG.Gateway = config.Gateway{Passthrough: true, SanitizeMode: "harness"}
	messages := make([]any, 0, 24)
	for i := 0; i < 12; i++ {
		messages = append(messages,
			map[string]any{"role": "user", "content": strings.Repeat("Please read the file and explain the architecture. ", 30)},
			map[string]any{"role": "assistant", "content": strings.Repeat("Here is the analysis of the sandbox and escalation path. ", 30)},
		)
	}
	body := map[string]any{"model": "glm-5.3", "stream": true, "messages": messages}
	raw, _ := json.Marshal(body)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := PrepareChatBody(raw); err != nil {
			b.Fatal(err)
		}
	}
}
