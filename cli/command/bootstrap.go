package command

import (
	"codebuddy-gateway/core"
	"codebuddy-gateway/global"
	"codebuddy-gateway/model"

	"github.com/spf13/cobra"
)

func Bootstrap(cmd *cobra.Command) {
	configFile, _ := cmd.Flags().GetString("config")
	apiKey, _ := cmd.Flags().GetString("api-key")
	adminKey, _ := cmd.Flags().GetString("admin-key")
	dev, _ := cmd.Flags().GetBool("dev")
	global.CORE_DEV = dev

	global.CORE_VP = core.Viper(configFile)
	core.ApplyKeyOverrides(apiKey, adminKey)
	if global.CORE_LOG == nil {
		global.CORE_LOG = core.Zap()
	}
	if global.CORE_DB == nil {
		global.CORE_DB = core.InitDB()
		if err := model.AutoMigrate(global.CORE_DB); err != nil {
			global.CORE_LOG.Fatal("database migrate failed: " + err.Error())
		}
	}
}

// HasAccounts 初始化配置和数据库，仅用于桌面启动入口判断是否需要首次登录。
// 调用方会在进程结束前继续使用该数据库，或直接退出进程。
func HasAccounts(configPath string) (bool, error) {
	cmd := &cobra.Command{}
	cmd.Flags().String("config", configPath, "")
	cmd.Flags().String("api-key", "", "")
	cmd.Flags().String("admin-key", "", "")
	cmd.Flags().Bool("dev", false, "")
	Bootstrap(cmd)
	accounts, err := model.ListAccounts()
	core.CloseDB()
	if err != nil {
		return false, err
	}
	return len(accounts) > 0, nil
}
