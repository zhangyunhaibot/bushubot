package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"bushubot-master/internal/alerter"
	"bushubot-master/internal/api"
	"bushubot-master/internal/bot"
	"bushubot-master/internal/config"
	"bushubot-master/internal/license"
	"bushubot-master/internal/store"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var Version = "dev"

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	log.Printf("bushubot-master %s 启动", Version)

	db, err := gorm.Open(postgres.Open(cfg.Database.DSN()), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}
	if err := runMigrations(db); err != nil {
		log.Fatalf("迁移失败: %v", err)
	}

	st := store.New(db)

	// 加载或生成 master 签名密钥（首次启动会自动生成 4096 位 RSA）
	priv, err := license.LoadOrGenerateKeypair(cfg.License.KeypairDir)
	if err != nil {
		log.Fatalf("加载/生成 master 密钥失败: %v", err)
	}
	pubPem, _ := license.PublicKeyPEM(&priv.PublicKey)
	log.Printf("master 公钥已就绪 (%s/master.pub):\n%s",
		cfg.License.KeypairDir, string(pubPem))

	// 主 Bot 先构造（agent API 要用它做日志通知）
	botH, err := bot.New(cfg.TelegramBot.Token, cfg.TelegramBot.AdminTGID, st, priv, cfg.License.DefaultDays)
	if err != nil {
		log.Fatalf("Bot 启动失败: %v", err)
	}

	// HTTP API
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", func(c *gin.Context) { c.String(200, "ok") })

	agentH := api.NewAgent(st, cfg.ReleaseRepo, &priv.PublicKey, botH)
	v1 := router.Group("/api/v1/agent")
	v1.Use(agentH.Auth())
	agentH.Register(v1)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("HTTP API 监听 :%d", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 启动失败: %v", err)
		}
	}()
	stopBot := make(chan struct{})
	go botH.Run(stopBot)

	// 升级失败自动告警
	stopAlerter := make(chan struct{})
	go alerter.New(st, botH).Run(stopAlerter)

	// 每天清理 30 天前的指标快照（避免无限增长）
	stopCleaner := make(chan struct{})
	go func() {
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-stopCleaner:
				return
			case <-t.C:
				if n, err := st.CleanupOldMetrics(); err != nil {
					log.Printf("清理旧指标失败: %v", err)
				} else if n > 0 {
					log.Printf("清理旧指标: %d 行", n)
				}
			}
		}
	}()

	// 等待退出
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("正在关闭服务...")

	close(stopBot)
	close(stopAlerter)
	close(stopCleaner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Println("服务已关闭")
}

// resolveMigrationsDir 解析 migrations 目录路径
// 优先级：环境变量 BUSHUBOT_MIGRATIONS_DIR > 二进制同目录的 migrations > 当前工作目录的 migrations
func resolveMigrationsDir() string {
	if v := os.Getenv("BUSHUBOT_MIGRATIONS_DIR"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "migrations")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
	}
	return "migrations"
}

// runMigrations 简单按文件名顺序执行 migrations 目录下的 SQL（幂等：用 IF NOT EXISTS）
// 路径解析顺序：环境变量 BUSHUBOT_MIGRATIONS_DIR > 二进制同目录的 migrations > 相对 migrations
func runMigrations(db *gorm.DB) error {
	dir := resolveMigrationsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("读取 migrations 失败 (%s): %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		log.Printf("执行迁移: %s", name)
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if err := db.Exec(string(content)).Error; err != nil {
			return fmt.Errorf("%s 失败: %w", name, err)
		}
	}
	return nil
}
