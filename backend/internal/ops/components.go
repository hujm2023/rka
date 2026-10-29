package ops

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"rka/internal/httpx"
)

func (s *Service) requireComponentQuiescence() error {
	// Only the offline CLI can acknowledge quiescence; a server environment flag
	// must never turn an HTTP backup into an unsafe live SQLite/file copy.
	if s.cfg.Mode == "backup" && os.Getenv("RKA_BACKUP_QUIESCED") == "true" {
		return nil
	}
	for _, root := range []string{s.cfg.TeachingDir(), s.cfg.GatewayDir()} {
		entries, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if len(entries) > 0 {
			return httpx.NewError(http.StatusConflict, "backup_requires_quiescence", "教学或网关已有持久数据；请运行 scripts/backup.sh 协调停写并创建完整备份。")
		}
	}
	if s.cfg.Teaching.DeepTutorURL != "" {
		return httpx.NewError(http.StatusConflict, "backup_requires_quiescence", "教学服务已配置；请通过 scripts/backup.sh 创建完整备份。")
	}
	return nil
}

func (s *Service) copyComponents(ctx context.Context, dir string) (map[string][]backupFile, error) {
	result := map[string][]backupFile{}
	for name, root := range map[string]string{"teaching": s.cfg.TeachingDir(), "gateway": s.cfg.GatewayDir()} {
		files := []backupFile{}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if path == root && os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s 包含非普通文件，不能安全快照：%s", name, path)
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			dst, err := safeJoin(filepath.Join(dir, "components", name), rel)
			if err != nil {
				return err
			}
			size, sha, err := copyFileVerified(path, dst, "", -1)
			if err != nil {
				return err
			}
			files = append(files, backupFile{RelPath: filepath.ToSlash(rel), Size: size, SHA256: sha})
			return nil
		})
		if err != nil {
			return nil, err
		}
		result[name] = files
	}
	return result, nil
}

func componentFiles(backupDir string, manifest backupManifest, visit func(string, string, backupFile) error) error {
	if manifest.Components == nil {
		if manifest.Teaching["status"] == "inventory_only" || manifest.Gateway["status"] == "configured" {
			return fmt.Errorf("旧备份只有教学/网关清单，没有数据文件；不能作为全平台恢复备份")
		}
		return nil
	}
	for name, files := range manifest.Components {
		if name != "teaching" && name != "gateway" {
			return fmt.Errorf("未知备份组件 %q", name)
		}
		for _, file := range files {
			path, err := safeJoin(filepath.Join(backupDir, "components", name), file.RelPath)
			if err != nil {
				return err
			}
			if file.SHA256 == "" {
				return fmt.Errorf("组件 %s 缺少校验和", name)
			}
			if err := verifyFileHash(path, file.SHA256, file.Size); err != nil {
				return err
			}
			if visit != nil {
				if err := visit(name, path, file); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func restoreComponents(backupDir, target string, manifest backupManifest) error {
	return componentFiles(backupDir, manifest, func(name, src string, file backupFile) error {
		dst, err := safeJoin(filepath.Join(target, name), file.RelPath)
		if err != nil {
			return err
		}
		_, _, err = copyFileVerified(src, dst, file.SHA256, file.Size)
		return err
	})
}
