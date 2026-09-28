package service

import "strings"

// messageText 提取 OpenAI 兼容消息里的纯文本，供协议转换识别摘要和用户消息。
func messageText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		chunks := make([]string, 0, len(v))
		for _, item := range v {
			switch part := item.(type) {
			case string:
				chunks = append(chunks, part)
			case map[string]any:
				if text, ok := part["text"].(string); ok && text != "" {
					chunks = append(chunks, text)
				}
			}
		}
		return strings.Join(chunks, "\n")
	default:
		return ""
	}
}
