package model

import (
	"codebuddy-gateway/global"

	"gorm.io/gorm"
)

func AutoMigrate(db *gorm.DB) error {
	// 控制台与请求明细日志已移除；历史 usage_logs 数据允许清理。
	if err := db.Exec("DROP TABLE IF EXISTS usage_logs").Error; err != nil {
		return err
	}
	if err := db.AutoMigrate(&Account{}, &LLMModel{}); err != nil {
		return err
	}
	if err := backfillManuallyDisabled(db); err != nil {
		return err
	}
	return SeedDefaultModels(db)
}

func backfillManuallyDisabled(db *gorm.DB) error {
	return db.Model(&Account{}).
		Where("status = ? AND manually_disabled = ?", AccountStatusDisabled, false).
		Update("manually_disabled", true).Error
}

func MustDB() *gorm.DB {
	return global.CORE_DB
}
