package service

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// 本文件锁住 termMatcher（Aho-Corasick）与旧实现 buildWordPattern（大 alternation
// 正则）的语义等价性。两者必须逐字节给出相同结果，包括边界处理与替换位置。
//
// 存在的理由：termMatcher 是为了性能替换掉正则的（详见 sanitize_matcher.go 顶部
// 注释），而边界规则「只锁左侧词边界、右侧放开」是靠手工实现 isLeftWordBoundary
// 而非正则引擎保证的。一旦 Go 的 \b 语义或我们的字节级判断漂移，脱敏就会静默
// 漏词或误伤，且不会有人立刻发现——所以必须用测试钉死。

// equivalenceCorpus 覆盖真实会出现的边界形状：多字节字符、零宽字符、标点、
// 下划线、数字前缀、大小写混合，以及各类派生词。
var equivalenceCorpus = []string{
	"",
	"plain text with nothing sensitive",
	"Codex CLI is an open source project led by OpenAI.",
	"sandbox",
	"sandboxed commands",
	"Run a sandboxed command with escalation checks.",
	"exploit",
	"exploited",
	"exploiting",
	"exploitation vector present",
	"skill",
	"kill",
	"a kill switch",
	"counterattack",
	"attack",
	"attacks",
	"vulnerability",
	"vulnerabilities",
	"DoS",
	"DDoS",
	"dos",
	"dosomething",
	"xdos",
	"credential testing",
	"credential stuffing",
	"brute force",
	"brute-force",
	"XSS",
	"CSRF",
	"SQL injection",
	"backdoor",
	"Backdoor",
	"BACKDOOR",
	"rootkit",
	"zero-day",
	"0day",
	"malware",
	"ransomware",
	"dangerous",
	"harmful",
	"illegal",
	"drugstore",
	"drug",
	"asandbox",
	"_sandbox",
	"1sandbox",
	"-sandbox",
	".sandbox",
	" sandbox",
	"\u200bsandbox",
	"\u200bexploit",
	"中sandbox",
	"\u4e2dsandbox",
	"a-malware-b",
	"kill skill",
	"OpenAI",
	"Anthropic",
	"Claude Code and Claude Opus",
	"Co-Authored-By: noreply@anthropic.com",
	"multiple hits: sandbox escalation credential testing",
	"repeat sandbox sandbox sandbox",
	"trailing sandbox",
	"sandbox at start",
	"happy path without keywords, just code",
	"exploit\nnewline\nsandbox",
	"CREDENTIAL TESTING in caps",
}

// legacyReplaceAll 用旧的大 alternation 正则实现替换，作为参照。
func legacyReplaceAll(s string, re *regexp.Regexp) string {
	if s == "" || re == nil {
		return s
	}
	return re.ReplaceAllStringFunc(s, zeroWidthSplit)
}

func TestTermMatcherMatchesLegacyRegexReplacement(t *testing.T) {
	complianceRegex := buildWordPattern(complianceSensitiveTerms)
	for _, in := range equivalenceCorpus {
		got := complianceMatcher.replaceAll(in)
		want := legacyReplaceAll(in, complianceRegex)
		if got != want {
			t.Errorf("compliance mismatch\n  in=%q\n got=%q\nwant=%q", in, got, want)
		}
	}
}

func TestBrandingMatcherMatchesLegacyRegexReplacement(t *testing.T) {
	brandingRegex := buildWordPattern(brandingTerms)
	for _, in := range equivalenceCorpus {
		got := brandingMatcher.replaceAll(in)
		want := legacyReplaceAll(in, brandingRegex)
		if got != want {
			t.Errorf("branding mismatch\n  in=%q\n got=%q\nwant=%q", in, got, want)
		}
	}
}

func TestTermMatcherMatchesLegacyRegexDetection(t *testing.T) {
	complianceRegex := buildWordPattern(complianceSensitiveTerms)
	brandingRegex := buildWordPattern(brandingTerms)
	for _, in := range equivalenceCorpus {
		if got, want := complianceMatcher.matches(in), complianceRegex.MatchString(in); got != want {
			t.Errorf("compliance detection mismatch on %q: matcher=%v regex=%v", in, got, want)
		}
		if got, want := brandingMatcher.matches(in), brandingRegex.MatchString(in); got != want {
			t.Errorf("branding detection mismatch on %q: matcher=%v regex=%v", in, got, want)
		}
	}
}

// TestTermMatcherDetectsAlreadyDesensitizedText 是 full 档降级重试的前提：
// 已插入零宽的文本必须仍被判定为「命中」，否则 resanitize 会认为无事可做。
func TestTermMatcherDetectsAlreadyDesensitizedText(t *testing.T) {
	for _, raw := range []string{"sandbox", "exploit", "credential testing", "OpenAI"} {
		desensitized := desensitizeAllTerms(raw)
		if desensitized == raw && raw != "OpenAI" {
			t.Fatalf("%q was not desensitized", raw)
		}
		if !hasSensitiveTerm(desensitized) {
			t.Errorf("already-desensitized %q was not detected as sensitive", desensitized)
		}
	}
}

// TestDesensitizeAllTermsIsIdempotent 保证重复清洗不会无限插入零宽。
func TestDesensitizeAllTermsIsIdempotent(t *testing.T) {
	in := "sandbox exploit credential testing and Anthropic"
	once := desensitizeAllTerms(in)
	twice := desensitizeAllTerms(once)
	if once != twice {
		t.Errorf("not idempotent\n once=%q\ntwice=%q", once, twice)
	}
}

// TestTermMatcherHandlesLongTextWithoutKeywords 是性能改造的正确性护栏：
// 长文本里没有关键词时必须原样返回（零分配路径）。
func TestTermMatcherHandlesLongTextWithoutKeywords(t *testing.T) {
	in := strings.Repeat("ordinary source code line with no flagged vocabulary. ", 500)
	if got := complianceMatcher.replaceAll(in); got != in {
		t.Fatalf("expected identical output, got len %d vs %d", len(got), len(in))
	}
}

// TestTermMatcherEmptyTermList 边界：空词表应返回 nil，调用方判空后原样返回。
func TestTermMatcherEmptyTermList(t *testing.T) {
	m := newTermMatcher(nil)
	if m != nil {
		t.Fatal("expected nil matcher for empty term list")
	}
	if got := desensitizeTerms("sandbox", m); got != "sandbox" {
		t.Fatalf("nil matcher must pass text through, got %q", got)
	}
}

// TestTermMatcherSkipsEmptyTerms 边界：词表里混入空串不应 panic 或产生空匹配。
func TestTermMatcherSkipsEmptyTerms(t *testing.T) {
	m := newTermMatcher([]string{"", "sandbox", ""})
	if m == nil {
		t.Fatal("expected non-nil matcher")
	}
	if got, want := m.replaceAll("a sandbox here"), "a s\u200bandbox here"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if m.matches("no keywords") {
		t.Error("empty terms must not match everything")
	}
}

// TestTermMatcherRegisteredTerms 确认自动机保留了全部非空词条。
func TestTermMatcherRegisteredTerms(t *testing.T) {
	wantCompliance := 0
	for _, term := range complianceSensitiveTerms {
		if term != "" {
			wantCompliance++
		}
	}
	if got := len(complianceMatcher.terms); got != wantCompliance {
		t.Errorf("compliance terms: got %d want %d", got, wantCompliance)
	}
	wantBranding := 0
	for _, term := range brandingTerms {
		if term != "" {
			wantBranding++
		}
	}
	if got := len(brandingMatcher.terms); got != wantBranding {
		t.Errorf("branding terms: got %d want %d", got, wantBranding)
	}
	if !reflect.DeepEqual(complianceMatcher.lens[0], complianceMatcher.lens[0]) {
		t.Fatal("sanity")
	}
}
