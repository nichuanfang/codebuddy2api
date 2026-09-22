package config

// Passwordless 控制下游 OpenAI 兼容接口是否免 API Key。
// 默认关闭；开启后请只监听 127.0.0.1，避免局域网未授权调用。
type Passwordless struct {
	Enabled bool `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
}
