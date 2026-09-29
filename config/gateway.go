package config

import (
	"reflect"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
)

type Gateway struct {
	APIKey                   string       `mapstructure:"api-key" json:"api-key" yaml:"api-key"`
	Upstream                 string       `mapstructure:"upstream" json:"upstream" yaml:"upstream"`
	Gzip                     bool         `mapstructure:"gzip" json:"gzip" yaml:"gzip"`
	Passthrough              bool         `mapstructure:"passthrough" json:"passthrough" yaml:"passthrough"`
	InjectReasoning          bool         `mapstructure:"inject-reasoning" json:"inject-reasoning" yaml:"inject-reasoning"`
	TimeoutSeconds           int          `mapstructure:"timeout-seconds" json:"timeout-seconds" yaml:"timeout-seconds"`
	StreamIdleTimeoutSeconds int          `mapstructure:"stream-idle-timeout-seconds" json:"stream-idle-timeout-seconds" yaml:"stream-idle-timeout-seconds"`
	MaxRetries               int          `mapstructure:"max-retries" json:"max-retries" yaml:"max-retries"`
	Rotate                   string       `mapstructure:"rotate" json:"rotate" yaml:"rotate"`
	TrustEnvProxy            bool         `mapstructure:"trust-env-proxy" json:"trust-env-proxy" yaml:"trust-env-proxy"`
	Proxy                    string       `mapstructure:"proxy" json:"proxy" yaml:"proxy"`
	ModelAlias               []ModelAlias `mapstructure:"model-alias" json:"model-alias" yaml:"model-alias"`
	FallbackModel            string       `mapstructure:"fallback-model" json:"fallback-model" yaml:"fallback-model"`
	// Models 是 /v1/models 的显式覆盖列表。为空时沿用 CodeBuddy 实时目录
	// 与内置兜底目录；配置后该列表优先，且顺序即为返回顺序。
	Models []ModelEntry `mapstructure:"models" json:"models" yaml:"models"`
	// SanitizeMode 控制发往上游前的清洗力度：
	//   ""/"harness"（默认）——覆盖全部常见触发面：客户端模板（system/developer）、
	//                          harness 注入的 user 上下文、tool 定义，以及随会话累积的
	//                          assistant 历史与 tool 输出（后两者只做不可见脱敏，不删改文字）。
	//                          默认值即面向「装上就用、写代码不被打断」，通常无需调整。
	//   "full"               ——额外允许删除被污染的模板文本（句子级剪枝）。
	//   "off"                ——完全关闭，仅用于排障对比。
	// 无论取何值，被上游 11128 拒绝时都会自动升到 full 重发一次。
	SanitizeMode    string `mapstructure:"sanitize-mode" json:"sanitize-mode" yaml:"sanitize-mode"`
	CountTokensMode string `mapstructure:"count-tokens-mode" json:"count-tokens-mode" yaml:"count-tokens-mode"`
}

// CountTokensModeName selects local estimation by default; upstream mode is opt-in.
func (g Gateway) CountTokensModeName() string {
	switch strings.ToLower(strings.TrimSpace(g.CountTokensMode)) {
	case "upstream":
		return "upstream"
	default:
		return "estimate"
	}
}

// SanitizeModeName 归一化配置值，回落到默认力度。
func (g Gateway) SanitizeModeName() string {
	switch strings.ToLower(strings.TrimSpace(g.SanitizeMode)) {
	case "off":
		return "off"
	case "full":
		return "full"
	default:
		return "harness"
	}
}

// ModelEntry 让用户在 config.yaml 里手工维护 /v1/models 的展示列表。
// 直接写 model-id 字符串，或用 {id, display-name} 形式补充显示名。
type ModelEntry struct {
	ID          string `mapstructure:"id" json:"id" yaml:"id"`
	DisplayName string `mapstructure:"display-name" json:"display-name" yaml:"display-name"`
}

func (m *ModelEntry) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var id string
	if err := unmarshal(&id); err == nil {
		m.ID = id
		return nil
	}
	type raw ModelEntry
	var r raw
	if err := unmarshal(&r); err != nil {
		return err
	}
	*m = ModelEntry(r)
	return nil
}

// Normalize 去掉空白 ID，并在只有 display-name 时兜底用 ID 作为标识。
func (m ModelEntry) Normalize() (ModelEntry, bool) {
	m.ID = strings.TrimSpace(m.ID)
	m.DisplayName = strings.TrimSpace(m.DisplayName)
	if m.ID == "" {
		return ModelEntry{}, false
	}
	if m.DisplayName == "" {
		m.DisplayName = m.ID
	}
	return m, true
}

type ModelAlias struct {
	From string `mapstructure:"from" json:"from" yaml:"from"`
	To   string `mapstructure:"to" json:"to" yaml:"to"`
}

// ConfiguredModels 返回配置里手工维护的模型列表（已归一化）。
// 第二个返回值表示用户是否真的配置过 models 覆盖项。
func (g Gateway) ConfiguredModels() ([]ModelEntry, bool) {
	if len(g.Models) == 0 {
		return nil, false
	}
	out := make([]ModelEntry, 0, len(g.Models))
	seen := map[string]struct{}{}
	for _, m := range g.Models {
		entry, ok := m.Normalize()
		if !ok {
			continue
		}
		if _, dup := seen[entry.ID]; dup {
			continue
		}
		seen[entry.ID] = struct{}{}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// ModelEntryDecodeHook 让 mapstructure（viper.Unmarshal）也能接受
// gateway.models 里的裸字符串写法。
func ModelEntryDecodeHook(from reflect.Type, to reflect.Type, data any) (any, error) {
	if to != reflect.TypeOf(ModelEntry{}) {
		return data, nil
	}
	if from.Kind() == reflect.String {
		return ModelEntry{ID: strings.TrimSpace(data.(string))}, nil
	}
	return data, nil
}

// ModelDecodeHook 供 viper.Unmarshal 使用：把 YAML 标量字符串
// （gateway.models 里的裸 model-id）解码成 ModelEntry。
func ModelDecodeHook() mapstructure.DecodeHookFunc {
	return mapstructure.ComposeDecodeHookFunc(ModelEntryDecodeHook)
}

func (g Gateway) UpstreamBase() string {
	if g.Upstream == "" {
		return "https://copilot.tencent.com"
	}
	return g.Upstream
}
func (g Gateway) Timeout() int {
	if g.TimeoutSeconds <= 0 {
		return 300
	}
	return g.TimeoutSeconds
}
func (g Gateway) Retries() int {
	if g.MaxRetries <= 0 {
		return 3
	}
	return g.MaxRetries
}
func (g Gateway) StreamIdleTimeout() time.Duration {
	if g.StreamIdleTimeoutSeconds <= 0 {
		return 300 * time.Second
	}
	return time.Duration(g.StreamIdleTimeoutSeconds) * time.Second
}

type CodeBuddy struct {
	IDEType          string `mapstructure:"ide-type" json:"ide-type" yaml:"ide-type"`
	IDEVersion       string `mapstructure:"ide-version" json:"ide-version" yaml:"ide-version"`
	ProductVersion   string `mapstructure:"product-version" json:"product-version" yaml:"product-version"`
	Product          string `mapstructure:"product" json:"product" yaml:"product"`
	Domain           string `mapstructure:"domain" json:"domain" yaml:"domain"`
	EnvID            string `mapstructure:"env-id" json:"env-id" yaml:"env-id"`
	AgentIntent      string `mapstructure:"agent-intent" json:"agent-intent" yaml:"agent-intent"`
	DefaultMaxTokens int    `mapstructure:"default-max-tokens" json:"default-max-tokens" yaml:"default-max-tokens"`
	BillingBase      string `mapstructure:"billing-base" json:"billing-base" yaml:"billing-base"`
}

func (c CodeBuddy) HeaderIDEType() string {
	if c.IDEType == "" {
		return "CodeBuddyIDE"
	}
	return c.IDEType
}
func (c CodeBuddy) HeaderIDEVersion() string {
	if c.IDEVersion == "" {
		return "4.9.7"
	}
	return c.IDEVersion
}
func (c CodeBuddy) HeaderProductVersion() string {
	if c.ProductVersion != "" {
		return c.ProductVersion
	}
	return c.HeaderIDEVersion()
}
func (c CodeBuddy) HeaderProduct() string {
	if c.Product == "" {
		return "SaaS"
	}
	return c.Product
}
func (c CodeBuddy) HeaderDomain() string {
	if c.Domain == "" {
		return "www.codebuddy.cn"
	}
	return c.Domain
}
func (c CodeBuddy) HeaderEnvID() string {
	if c.EnvID == "" {
		return "production"
	}
	return c.EnvID
}
func (c CodeBuddy) HeaderAgentIntent() string {
	if c.AgentIntent == "" {
		return "craft"
	}
	return c.AgentIntent
}
func (c CodeBuddy) BillingBaseURL() string {
	if c.BillingBase == "" {
		return "https://www.codebuddy.cn"
	}
	return c.BillingBase
}

type Refresh struct {
	Enabled        bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Cron           string `mapstructure:"cron" json:"cron" yaml:"cron"`
	ThresholdDays  int    `mapstructure:"threshold-days" json:"threshold-days" yaml:"threshold-days"`
	TimeoutSeconds int    `mapstructure:"timeout-seconds" json:"timeout-seconds" yaml:"timeout-seconds"`
}

func (r Refresh) Spec() string {
	if r.Cron == "" {
		return "0 3 * * *"
	}
	return r.Cron
}
func (r Refresh) Threshold() int {
	if r.ThresholdDays <= 0 {
		return 30
	}
	return r.ThresholdDays
}
func (r Refresh) Timeout() int {
	if r.TimeoutSeconds <= 0 {
		return 15
	}
	return r.TimeoutSeconds
}

type Watchdog struct {
	Enabled         bool `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	IntervalSeconds int  `mapstructure:"interval-seconds" json:"interval-seconds" yaml:"interval-seconds"`
	FailThreshold   int  `mapstructure:"fail-threshold" json:"fail-threshold" yaml:"fail-threshold"`
	CooldownSeconds int  `mapstructure:"cooldown-seconds" json:"cooldown-seconds" yaml:"cooldown-seconds"`
	HealthCheck     bool `mapstructure:"health-check" json:"health-check" yaml:"health-check"`
	SyncCredit      bool `mapstructure:"sync-credit" json:"sync-credit" yaml:"sync-credit"`
}

func (w Watchdog) Interval() int {
	if w.IntervalSeconds <= 0 {
		return 300
	}
	return w.IntervalSeconds
}
func (w Watchdog) FailLimit() int {
	if w.FailThreshold <= 0 {
		return 3
	}
	return w.FailThreshold
}
func (w Watchdog) Cooldown() int {
	if w.CooldownSeconds <= 0 {
		return 600
	}
	return w.CooldownSeconds
}
