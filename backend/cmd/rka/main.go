// Command rka is the single application image. The mode selects the run role:
//
//	rka serve           HTTP application (pages, static assets, API, events)
//	rka worker          background teaching-task worker
//	rka migrate         apply database migrations under an advisory lock
//	rka init            idempotent one-shot initialisation (dirs, secrets, schema, content, admin)
//	rka init-secrets    create the project-local secret key and bootstrap token
//	rka import-content  import the bundled content snapshot
//	rka backup          create a consistency backup set
//	rka verify-backup   verify a backup set in an isolated directory
//	rka restore         restore a backup set into a target directory
//	rka retention       apply the configured retention policy
//	rka doctor          print dependency and readiness diagnostics
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"rka/internal/config"
	"rka/internal/worker"
)

const usage = `rka — RKA 学习平台

用法: rka <mode>

模式:
  serve           启动 HTTP 应用（页面、静态资源、API、事件）
  worker          启动后台教学任务 Worker
  worker-health   检查当前 Worker 容器的轮询心跳
  migrate         执行数据库迁移（持有迁移锁）
  init            一次性初始化：目录、密钥、迁移、内容、首个管理员（可重复执行）
  init-secrets    生成项目内密钥与一次性初始化令牌
  import-content  导入内容快照
  backup          创建一致性备份
  verify-backup   在隔离目录验证备份
  restore         将备份恢复到新目录
  retention       执行保留策略
  doctor          输出依赖与就绪诊断
`

func main() {
	mode := "serve"
	if len(os.Args) > 1 {
		mode = strings.TrimSpace(os.Args[1])
	}
	switch mode {
	case "serve", "worker", "worker-health", "migrate", "init", "init-secrets", "import-content",
		"backup", "verify-backup", "restore", "retention", "doctor":
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "未知模式 %q\n\n%s", mode, usage)
		os.Exit(2)
	}

	// Initialisation may create the secret key; every other mode must read it.
	if mode == "init" || mode == "init-secrets" {
		os.Setenv("RKA_SECRET_KEY_AUTOGENERATE", "true")
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		os.Exit(2)
	}
	cfg.Mode = mode
	log := newLogger(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, log, mode); err != nil {
		if errors.Is(err, context.Canceled) {
			log.Info("stopped")
			return
		}
		log.Error("command failed", "mode", mode, "error", err)
		os.Exit(1)
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	options := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stdout, options)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, options)
	}
	return slog.New(handler).With("service", "rka", "mode", cfg.Mode)
}

func run(ctx context.Context, cfg *config.Config, log *slog.Logger, mode string) error {
	switch mode {
	case "serve":
		return runServe(ctx, cfg, log)
	case "worker":
		return runWorker(ctx, cfg, log)
	case "worker-health":
		return worker.CheckHealth(cfg)
	case "migrate":
		return runMigrate(ctx, cfg, log)
	case "init":
		return runInit(ctx, cfg, log)
	case "init-secrets":
		return runInitSecrets(cfg, log)
	case "import-content":
		return runImportContent(ctx, cfg, log)
	case "backup":
		return runBackup(ctx, cfg, log)
	case "verify-backup":
		return runVerifyBackup(ctx, cfg, log, os.Args[2:])
	case "restore":
		return runRestore(ctx, cfg, log, os.Args[2:])
	case "retention":
		return runRetention(ctx, cfg, log)
	case "doctor":
		return runDoctor(ctx, cfg, log)
	}
	return fmt.Errorf("unsupported mode %s", mode)
}

func timeoutContext(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, d)
}
