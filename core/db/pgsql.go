package db

import (
	"fmt"

	"codebuddy-gateway/global"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Pgsql PostgreSQL 数据库
type Pgsql struct{}

// Connect 连接 PostgreSQL
func (p *Pgsql) Connect() *gorm.DB {
	cfg := global.CORE_CONFIG.Pgsql
	dsn := cfg.Dsn()

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                                   newGormLogger(cfg.LogMode),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		panic(fmt.Sprintf("PostgreSQL 连接失败: %v", err))
	}

	sqlDB, _ := db.DB()
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)

	return db
}
