package config

type System struct {
	Db         string `mapstructure:"db" json:"db" yaml:"db"`                         // 数据库类型: sqlite, pgsql, mysql
	ListenAddr string `mapstructure:"listenAddr" json:"listenAddr" yaml:"listenAddr"` // 监听地址
	// AllowedOrigins 是允许跨域调用的 Origin 白名单（不带路径，如 "https://app.example.com"）。
	// 为空时回退到旧行为：反射请求 Origin 且不带 credentials——浏览器脚本可调用，
	// 但不会携带 cookie/HTTP 认证信息，对纯 Bearer-token 网关是可接受的默认。
	// 配置后仅命中白名单的 Origin 会被反射并带上 Access-Control-Allow-Credentials。
	AllowedOrigins []string `mapstructure:"allowed-origins" json:"allowed-origins" yaml:"allowed-origins"`
}
