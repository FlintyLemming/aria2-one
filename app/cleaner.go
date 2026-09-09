package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IsSafeSubpath 检查 targetPath 规范化后是否严格位于 baseDir 之下
func IsSafeSubpath(baseDir, targetPath string) (string, bool) {
	cleanBase := filepath.Clean(baseDir)
	cleanTarget := filepath.Clean(targetPath)

	if cleanTarget == cleanBase {
		return "", false
	}

	// 必须以 baseDir + 路径分隔符 开头
	prefix := cleanBase
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}

	if !strings.HasPrefix(cleanTarget, prefix) {
		return "", false
	}

	return cleanTarget, true
}

// RemoveTaskFiles 删除给定的文件列表、伴生 .aria2 文件，并自底向上清理因此产生的空目录
func RemoveTaskFiles(downloadDir string, filePaths []string) ([]string, error) {
	cleanBase := filepath.Clean(downloadDir)
	var deleted []string
	var parentDirsToCheck []string

	for _, rawPath := range filePaths {
		if rawPath == "" {
			continue
		}
		target, ok := IsSafeSubpath(cleanBase, rawPath)
		if !ok {
			// 跳过非法/逃逸路径
			continue
		}

		// 1. 删除目标文件
		if err := os.Remove(target); err == nil {
			deleted = append(deleted, target)
		} else if !os.IsNotExist(err) {
			return deleted, fmt.Errorf("failed to remove %s: %w", target, err)
		}

		// 2. 检查并删除控制文件 (.aria2)
		ariaFile := target + ".aria2"
		if err := os.Remove(ariaFile); err == nil {
			deleted = append(deleted, ariaFile)
		}

		// 记录其父目录用于后续空目录清理
		parentDirsToCheck = append(parentDirsToCheck, filepath.Dir(target))
	}

	// 3. 自底向上检查并清理空文件夹
	for _, dir := range parentDirsToCheck {
		cleanDir := filepath.Clean(dir)
		for cleanDir != cleanBase && strings.HasPrefix(cleanDir, cleanBase+string(filepath.Separator)) {
			entries, err := os.ReadDir(cleanDir)
			if err != nil {
				break
			}
			if len(entries) == 0 {
				if err := os.Remove(cleanDir); err != nil {
					break
				}
				cleanDir = filepath.Dir(cleanDir)
			} else {
				break
			}
		}
	}

	return deleted, nil
}
