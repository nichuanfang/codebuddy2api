package service

import "strings"

// termMatcher 是面向「多词字面量匹配」的 Aho-Corasick 自动机。
//
// 存在的理由：sanitize.go 的词表（complianceSensitiveTerms 约 80 条，加上
// brandingTerms）原先被编译成单个 `\b(?:a|b|c...)` 大 alternation。因为词表
// 刻意只锁左侧词边界、右侧放开（要覆盖 exploit / exploited / exploitation），
// Go 的 RE2 无法提取公共前缀做加速，必须退回「每个位置尝试全部分支」的线性扫描。
//
// 实测（约 850KB 文本、约 80 条词表）：
//
//	大 alternation        23.6 ms（2.4 MB/s）
//	Aho-Corasick          1.7 ms 命中 / 0.79 ms 未命中（31~41 MB/s）
//
// 两者在匹配区间与替换结果上逐字节等价，由 sanitize_matcher_test.go 钉死。
type termMatcher struct {
	nodes  []termNode
	terms  []string
	lens   []int
	maxLen int
}

type termNode struct {
	next   map[byte]int32
	fail   int32
	termID int32
}

// newTermMatcher 用词表构建自动机。词表为空时返回 nil，调用方需判空。
func newTermMatcher(terms []string) *termMatcher {
	if len(terms) == 0 {
		return nil
	}
	m := &termMatcher{
		nodes: []termNode{{fail: 0, termID: -1}},
	}
	for _, term := range terms {
		// 词表构建期防御：空串会变成「到处都命中」的零长匹配。
		if term == "" {
			continue
		}
		id := int32(len(m.terms))
		m.terms = append(m.terms, term)
		m.lens = append(m.lens, len(term))
		if len(term) > m.maxLen {
			m.maxLen = len(term)
		}

		// 自动机按小写字节建边，匹配时同样把输入小写，等价于原正则的 (?i)。
		// 词表只含 ASCII 与多字节 UTF-8：UTF-8 续字节都 >= 0x80，
		// lowerByte 不会改写它们，因此逐字节小写对多字节字符是安全的。
		cur := int32(0)
		for i := 0; i < len(term); i++ {
			b := lowerByte(term[i])
			if m.nodes[cur].next == nil {
				m.nodes[cur].next = make(map[byte]int32, 4)
			}
			next, ok := m.nodes[cur].next[b]
			if !ok {
				m.nodes = append(m.nodes, termNode{termID: -1})
				next = int32(len(m.nodes) - 1)
				m.nodes[cur].next[b] = next
			}
			cur = next
		}
		// 多个词共享同一终点时（互为前缀/后缀关系），保留先注册的。
		// 被覆盖的词仍能通过 fail 链在其它位置上被命中，不会静默丢失。
		if m.nodes[cur].termID < 0 {
			m.nodes[cur].termID = id
		}
	}
	m.buildFailureLinks()
	return m
}

// buildFailureLinks 按 BFS 为每个节点计算 fail 指针：
// fail 指向「当前已匹配串的最长真后缀，且该后缀对应自动机中的某条路径」。
func (m *termMatcher) buildFailureLinks() {
	queue := make([]int32, 0, len(m.nodes))
	for _, child := range m.nodes[0].next {
		m.nodes[child].fail = 0
		queue = append(queue, child)
	}
	for head := 0; head < len(queue); head++ {
		cur := queue[head]
		for b, child := range m.nodes[cur].next {
			// 沿 cur 的 fail 链找一个能接上 b 的节点。
			fail := m.nodes[cur].fail
			for fail != 0 {
				if next, ok := m.nodes[fail].next[b]; ok {
					m.nodes[child].fail = next
					break
				}
				fail = m.nodes[fail].fail
			}
			if m.nodes[child].fail == 0 && cur != 0 {
				if next, ok := m.nodes[0].next[b]; ok {
					m.nodes[child].fail = next
				}
			}
			queue = append(queue, child)
		}
	}
}

// findFrom 在 s[from:] 中查找下一个匹配，返回起始位置与字节长度。
//
// 语义必须与 Go regexp 严格一致，因为 termMatcher 是原 `\b(?:...)` 正则的替代：
//   - 最左优先：返回起始位置最小的匹配（即使存在结束位置更早但起点更靠右的匹配）。
//     典型例子 "noreply@anthropic.com"：`anthropic` 结束更早，但 `noreply@anthropic.com`
//     起点更靠左，所以必须优先返回后者。
//   - 同起点取最长：同一 start 下有多个词命中时保留最长者。
//   - 左边界：start 处必须满足 \b（见 isLeftWordBoundary）。
//
// 实现上维护一个待定候选，并在扫描位置越过 candStart+maxLen 时结算。
// 这个界是安全的：任何起点 < candStart 的匹配，其结束位置必不超过 candStart+maxLen，
// 越过该点后不可能再出现更靠左的候选。同时它保证线性复杂度——每个字节最多被
// 回看 maxLen 次，而不是每轮重扫到字符串末尾。
func (m *termMatcher) findFrom(s string, from int) (int, int, bool) {
	if m == nil || from >= len(s) {
		return 0, 0, false
	}
	cur := int32(0)
	candStart, candLen := -1, 0
	for i := from; i < len(s); i++ {
		b := lowerByte(s[i])
		for {
			if next, ok := m.nodes[cur].next[b]; ok {
				cur = next
				break
			}
			if cur == 0 {
				break
			}
			cur = m.nodes[cur].fail
		}
		// 沿 fail 链检查各级终点：短词可能作为当前已匹配长词的后缀命中。
		for node := cur; node != 0; node = m.nodes[node].fail {
			id := m.nodes[node].termID
			if id < 0 {
				continue
			}
			length := m.lens[id]
			start := i + 1 - length
			if start < from || !isLeftWordBoundary(s, start) {
				continue
			}
			if candStart < 0 || start < candStart || (start == candStart && length > candLen) {
				candStart, candLen = start, length
			}
		}
		if candStart >= 0 && i >= candStart+m.maxLen {
			return candStart, candLen, true
		}
	}
	if candStart >= 0 {
		return candStart, candLen, true
	}
	return 0, 0, false
}

// replaceAll 把 s 中所有命中词做零宽脱敏。未命中时原样返回且零分配。
func (m *termMatcher) replaceAll(s string) string {
	first, length, ok := m.findFrom(s, 0)
	if !ok {
		return s
	}
	var b strings.Builder
	// 命中词会插入 U+200B（3 字节）。
	b.Grow(len(s) + 3)
	b.WriteString(s[:first])
	for {
		b.WriteString(zeroWidthSplit(s[first : first+length]))
		pos := first + length
		next, nextLen, found := m.findFrom(s, pos)
		if !found {
			b.WriteString(s[pos:])
			return b.String()
		}
		b.WriteString(s[pos:next])
		first, length = next, nextLen
	}
}

// matches 只判断是否存在命中，不构造输出。
// 用于 hasSensitiveTerm 这类「只想知道要不要动手」的判定路径。
func (m *termMatcher) matches(s string) bool {
	_, _, ok := m.findFrom(s, 0)
	return ok
}

// isLeftWordBoundary 判断 s[start] 处是否构成正则 `\b` 的左边界。
//
// 必须与 Go regexp 的语义严格对齐：Go 的 `\b` 基于 ASCII `\w`（[0-9A-Za-z_]），
// 非 ASCII 一律不算词字符。因此：
//   - 前一字节是字母/数字/下划线 → 不是边界（`asandbox` 不命中 `sandbox`）
//   - 前一字节是空格、标点、UTF-8 续字节（含零宽空格 U+200B 的 0x8B 尾字节、
//     中文汉字的续字节）→ 是边界
//
// 这条规则决定了 `\u200bsandbox`（已被脱敏过的文本）仍会再次命中，与原正则一致，
// 也是 hasSensitiveTerm 能识别「已脱敏文本」、从而让 full 档降级重试得以触发的前提。
func isLeftWordBoundary(s string, start int) bool {
	if start <= 0 {
		return true
	}
	return !isWordByte(s[start-1])
}

// isWordByte 对齐 Go regexp 的 `\w`：仅 ASCII 字母、数字、下划线。
func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// lowerByte 把 ASCII 大写字母映射为小写；其它字节原样返回。
func lowerByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 32
	}
	return b
}

// attributionMarker 是三个归属正则共有的硬性字面量。
//
// codexOriginPattern / openAIAttributionPattern / openAIPlainAttributionPattern
// 都以 OpenAI 结尾，三条正则的字符串里都必然出现 "penAI"（零宽空格插在 O 与 p
// 之间，所以 ASCII 侧的可搜片段是 "penAI"）。它因此可以当作「这三条正则有没有
// 可能命中」的必要条件用来预筛。
const attributionMarker = "penai"

// containsFoldASCII 判断 s 是否包含 lowerSubstr，按 ASCII 大小写不敏感。
// lowerSubstr 必须已经是小写的字面量。
//
// 用途是做正则预筛：命中条件不成立时直接跳过后续的全量 regexp 扫描。
// 用裸字节循环而不是 strings.ToLower + Contains，是为了避免为长文本分配一份
// 完整的小写副本（实测长文本上零分配、约 2.6µs/8KB）。
func containsFoldASCII(s, lowerSubstr string) bool {
	n := len(lowerSubstr)
	if n == 0 || len(s) < n {
		return false
	}
	first := lowerSubstr[0]
	for i := 0; i+n <= len(s); i++ {
		if lowerByte(s[i]) != first {
			continue
		}
		match := true
		for j := 1; j < n; j++ {
			if lowerByte(s[i+j]) != lowerSubstr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
