package service

import (
	"regexp"
	"strings"
	"testing"
)

// TestContainsFoldASCII 覆盖大小写与边界，确保预筛本身不出错。
func TestContainsFoldASCII(t *testing.T) {
	cases := []struct {
		s, sub string
		want   bool
	}{
		{"OpenAI", "penai", true},
		{"openai", "penai", true},
		{"OPENAI", "penai", true},
		{"oPeNaI", "penai", true},
		{"leading OpenAI text", "penai", true},
		{"trailing text OpenAI", "penai", true},
		{"no mention of it", "penai", false},
		{"pen", "penai", false},
		{"", "penai", false},
		{"penAI", "penai", true},
		{"PENAI", "penai", true},
	}
	for _, c := range cases {
		if got := containsFoldASCII(c.s, c.sub); got != c.want {
			t.Errorf("containsFoldASCII(%q, %q) = %v, want %v", c.s, c.sub, got, c.want)
		}
	}
	if containsFoldASCII("anything", "") {
		t.Error("empty needle must not match")
	}
}

// TestAttributionPrescreenNeverMisses 是预筛安全性的核心护栏：
// 只要三个归属正则中任何一个能命中，预筛就必须为真。
// 反过来（预筛为真但正则不命中）是允许的，只是少省一点 CPU。
func TestAttributionPrescreenNeverMisses(t *testing.T) {
	patterns := []*regexp.Regexp{
		codexOriginPattern,
		openAIAttributionPattern,
		openAIPlainAttributionPattern,
	}
	corpus := []string{
		"Codex CLI is an open source project led by OpenAI.",
		"Codex CLI is an open-source project by OpenAI",
		"Codex CLI, an open source project from OpenAI,",
		"Codex CLI is developed by OpenAI",
		"maintained by OpenAI",
		"built by OpenAI",
		"by OpenAI",
		"from OpenAI",
		"of OpenAI",
		"OpenAI",
		"openai",
		"OPENAI",
		"nothing to see here",
		"pen",
		"Anthropic only",
		"",
	}
	for _, s := range corpus {
		anyMatch := false
		for _, re := range patterns {
			if re.MatchString(s) {
				anyMatch = true
				break
			}
		}
		if anyMatch && !containsFoldASCII(s, attributionMarker) {
			t.Errorf("prescreen missed a matching input %q", s)
		}
	}
}

// TestSanitizeTextSkipsAttributionWhenAbsent 确认预筛生效后行为不变：
// 不含归属句的文本经过 sanitizeText 后，不应有任何归属相关改写，
// 但零宽脱敏仍必须照常发生。
func TestSanitizeTextSkipsAttributionWhenAbsent(t *testing.T) {
	in := "Follow the sandbox rules and avoid escalation."
	out := sanitizeText(in, sanitizeHarness)
	if strings.Contains(out, "OpenAI") {
		t.Fatal("unexpected attribution text")
	}
	if !strings.Contains(out, "\u200b") {
		t.Fatalf("desensitization did not run: %q", out)
	}
	if stripInvisibleMarks(out) != in {
		t.Fatalf("text changed beyond invisible marks\n got=%q\nwant=%q", stripInvisibleMarks(out), in)
	}
}

// TestSanitizeTextStillRemovesAttribution 确认预筛没有把真实工作挡掉。
func TestSanitizeTextStillRemovesAttribution(t *testing.T) {
	in := "Codex CLI is an open source project led by OpenAI. Be precise."
	out := stripInvisibleMarks(sanitizeText(in, sanitizeHarness))
	if strings.Contains(out, "led by OpenAI") {
		t.Fatalf("attribution sentence was not removed: %q", out)
	}
	if !strings.Contains(out, "Be precise") {
		t.Fatalf("unrelated text was lost: %q", out)
	}
}

// TestSanitizeTextAttributionPrescreenEquivalence 端到端等价性：
// 加了预筛后，对同一输入的结果必须与「不做预筛」的参考实现逐字节一致。
func TestSanitizeTextAttributionPrescreenEquivalence(t *testing.T) {
	reference := func(s string) string {
		if s == "" {
			return s
		}
		s = codexOriginPattern.ReplaceAllString(s, "")
		s = openAIAttributionPattern.ReplaceAllString(s, "")
		s = openAIPlainAttributionPattern.ReplaceAllString(s, "")
		s = tidyAttributionArtifacts(s)
		s = desensitizeAllTerms(s)
		return tidySpacing(s)
	}
	corpus := []string{
		"Codex CLI is an open source project led by OpenAI.",
		"Codex CLI is an open-source project by OpenAI",
		"Codex CLI, an open source project from OpenAI,",
		"Codex CLI is developed by OpenAI",
		"maintained by OpenAI and more text",
		"by OpenAI",
		"from OpenAI",
		"of OpenAI",
		"OpenAI alone",
		"openai lowercase",
		"OPENAI uppercase",
		"nothing to see here",
		"Follow the sandbox rules and avoid escalation.",
		"pen without attribution",
		"Anthropic only",
		"",
		"mixed sandbox and OpenAI in one line",
	}
	for _, in := range corpus {
		got := sanitizeText(in, sanitizeHarness)
		want := reference(in)
		if got != want {
			t.Errorf("prescreen changed output for %q\n got=%q\nwant=%q", in, got, want)
		}
	}
}
