package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rka/internal/app"
	"rka/internal/config"
	"rka/internal/store"
)

func openApp(ctx context.Context, cfg *config.Config, log *slog.Logger) (*app.App, error) {
	db, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return nil, err
	}
	application, err := app.New(ctx, cfg, db, log)
	if err != nil {
		db.Close()
		return nil, err
	}
	return application, nil
}

func runServe(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	if err := requireSchema(ctx, cfg, log); err != nil {
		return err
	}
	application, err := openApp(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer application.DB.Close()
	return application.Run(ctx)
}

func runWorker(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	if err := requireSchema(ctx, cfg, log); err != nil {
		return err
	}
	application, err := openApp(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer application.DB.Close()
	log.Info("worker starting", "worker_id", cfg.Worker.ID)
	return application.Worker.Run(ctx)
}

func runMigrate(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	db, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()
	applied, err := db.Migrate(ctx)
	if err != nil {
		return err
	}
	version, _ := db.SchemaVersion(ctx)
	if len(applied) == 0 {
		log.Info("database schema up to date", "version", version)
	} else {
		log.Info("migrations applied", "versions", applied, "version", version)
	}
	return nil
}

// requireSchema refuses to start against a database that was not migrated by
// this binary, which is the compatibility gate before an upgrade or rollback.
func requireSchema(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	db, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()
	want, err := store.BinarySchemaVersion()
	if err != nil {
		return err
	}
	got, err := db.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version (run `rka migrate`): %w", err)
	}
	if got != want {
		return fmt.Errorf("数据库 schema 版本为 %d，本镜像要求 %d；请先运行 `rka migrate`，或在回退时使用兼容镜像", got, want)
	}
	return nil
}

func runInitSecrets(cfg *config.Config, log *slog.Logger) error {
	secretPath := filepath.Join(cfg.SecretsDir(), "secret-key")
	tokenPath, err := config.GenerateBootstrapToken()
	if err != nil {
		return err
	}
	log.Info("secrets ready", "secret_key", secretPath, "bootstrap_token", tokenPath)
	fmt.Printf("密钥文件: %s\n初始化令牌: %s\n\n把密钥文件路径写入 RKA_SECRET_KEY_FILE，或在部署环境设置 RKA_SECRET_KEY。\n首个管理员通过 /api/auth/bootstrap 使用初始化令牌建立。\n", secretPath, tokenPath)
	return nil
}

// runInit is safe to run on every start: it creates what is missing, imports
// content only when the snapshot version is not published yet, and never resets
// accounts or overwrites existing data.
func runInit(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	if _, err := config.GenerateBootstrapToken(); err != nil {
		return err
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()
	applied, err := db.Migrate(ctx)
	if err != nil {
		return err
	}
	log.Info("schema ready", "applied", applied)

	application, err := app.New(ctx, cfg, db, log)
	if err != nil {
		return err
	}
	snapshot := cfg.ContentSnapshot
	if _, statErr := os.Stat(filepath.Join(snapshot, "catalog.json")); statErr != nil {
		log.Warn("content snapshot not found; skipping import", "snapshot", snapshot)
	} else {
		importCtx, cancel := timeoutContext(ctx, 10*time.Minute)
		result, importErr := application.Content.ImportSnapshot(importCtx, snapshot, "")
		cancel()
		if importErr != nil {
			return fmt.Errorf("import content: %w", importErr)
		}
		log.Info("content import finished", "version", result.Version, "counts", result.Counts, "errors", len(result.Errors))
	}

	if email := strings.TrimSpace(os.Getenv("RKA_ADMIN_EMAIL")); email != "" {
		password := os.Getenv("RKA_ADMIN_PASSWORD")
		if password == "" {
			return errors.New("RKA_ADMIN_PASSWORD is required when RKA_ADMIN_EMAIL is set")
		}
		created, err := application.Auth.EnsureAdmin(ctx, email, password, strings.TrimSpace(os.Getenv("RKA_ADMIN_DISPLAY_NAME")))
		if err != nil {
			return fmt.Errorf("ensure admin: %w", err)
		}
		log.Info("admin account ensured", "email", email, "created", created)
	} else {
		log.Info("no RKA_ADMIN_EMAIL set; first admin must use the bootstrap token")
	}
	return nil
}

func runImportContent(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	application, err := openApp(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer application.DB.Close()
	snapshot := cfg.ContentSnapshot
	importCtx, cancel := timeoutContext(ctx, 10*time.Minute)
	defer cancel()
	result, err := application.Content.ImportSnapshot(importCtx, snapshot, "")
	if err != nil {
		return err
	}
	log.Info("content imported", "version", result.Version, "counts", result.Counts, "errors", result.Errors)
	return nil
}

func runBackup(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	application, err := openApp(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer application.DB.Close()
	backupCtx, cancel := timeoutContext(ctx, 30*time.Minute)
	defer cancel()
	backup, err := application.Ops.CreateBackup(backupCtx, "")
	if err != nil {
		return err
	}
	log.Info("backup created", "id", backup.ID, "path", backup.Path, "bytes", backup.Size, "sha256", backup.SHA256)
	fmt.Printf("%s\n", backup.Path)
	return nil
}

func runVerifyBackup(ctx context.Context, cfg *config.Config, log *slog.Logger, args []string) error {
	if len(args) < 1 {
		return errors.New("用法: rka verify-backup <backup-id|path>")
	}
	application, err := openApp(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer application.DB.Close()
	verifyCtx, cancel := timeoutContext(ctx, 30*time.Minute)
	defer cancel()
	report, err := application.Ops.VerifyBackup(verifyCtx, args[0])
	if err != nil {
		return err
	}
	log.Info("backup verified", "id", report.ID, "ok", report.OK, "checks", report.Checks, "errors", report.Errors)
	if !report.OK {
		return fmt.Errorf("备份校验失败: %s", strings.Join(report.Errors, "; "))
	}
	return nil
}

func runRestore(ctx context.Context, cfg *config.Config, log *slog.Logger, args []string) error {
	if len(args) < 2 {
		return errors.New("用法: rka restore <backup-id|path> <target-dir>")
	}
	application, err := openApp(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer application.DB.Close()
	restoreCtx, cancel := timeoutContext(ctx, 60*time.Minute)
	defer cancel()
	if err := application.Ops.RestoreBackup(restoreCtx, args[0], args[1]); err != nil {
		return err
	}
	log.Info("backup restored", "target", args[1])
	fmt.Printf("已恢复到 %s。\n下一步：1) 新建空数据库；2) pg_restore --dbname <新库> %s/db.dump；3) 以 RKA_DATA_DIR=%s、RKA_DATABASE_URL=<新库> 启动。\n", args[1], args[1], args[1])
	return nil
}

func runRetention(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	application, err := openApp(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer application.DB.Close()
	stats, err := application.Ops.RunRetention(ctx)
	if err != nil {
		return err
	}
	log.Info("retention applied", "stats", stats)
	return nil
}

func runDoctor(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	fmt.Printf("mode=%s data_dir=%s static_dir=%s addr=%s\n", cfg.Mode, cfg.DataDir, cfg.StaticDir, cfg.Addr)
	db, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		fmt.Printf("database: unreachable (%v)\n", err)
		return err
	}
	defer db.Close()
	want, _ := store.BinarySchemaVersion()
	got, err := db.SchemaVersion(ctx)
	fmt.Printf("database: ok; schema database=%d binary=%d\n", got, want)
	if err != nil {
		return err
	}
	var materials, questions, users int
	_ = db.Pool.QueryRow(ctx, `select count(*) from materials`).Scan(&materials)
	_ = db.Pool.QueryRow(ctx, `select count(*) from questions`).Scan(&questions)
	_ = db.Pool.QueryRow(ctx, `select count(*) from users`).Scan(&users)
	fmt.Printf("content: materials=%d questions=%d users=%d\n", materials, questions, users)
	fmt.Printf("teaching: %s\n", ifEmpty(cfg.Teaching.DeepTutorURL, "unconfigured"))
	fmt.Printf("model: %s\n", ifEmpty(cfg.AI.Endpoint, "unconfigured"))
	fmt.Printf("embedding: %s (%s, %d dims)\n", ifEmpty(cfg.Teaching.EmbeddingURL, "unconfigured"), cfg.Teaching.EmbeddingModel, cfg.Teaching.EmbeddingDims)
	if err := cfg.EnsureDirs(); err != nil {
		fmt.Printf("data dirs: %v\n", err)
		return err
	}
	fmt.Printf("data dirs: ok\n")
	return nil
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func ifEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
