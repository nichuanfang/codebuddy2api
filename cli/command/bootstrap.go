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
	logLevel, _ := cmd.Flags().GetString("log-level")
	global.CORE_VP = core.Viper(configFile)
	core.ApplyKeyOverrides(apiKey)
	core.ApplyLogLevelOverrides(logLevel)
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

func HasAccounts(configPath string) (bool, error) {
	cmd := &cobra.Command{}
	cmd.Flags().String("config", configPath, "")
	cmd.Flags().String("api-key", "", "")
	cmd.Flags().String("log-level", "", "")
	Bootstrap(cmd)
	accounts, err := model.ListAccounts()
	core.CloseDB()
	if err != nil {
		return false, err
	}
	return len(accounts) > 0, nil
}
