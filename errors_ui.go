package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
)

// friendlyError converts low-level technical errors (SQLite, zip, OS paths,
// TLS, HTTP codes, raw SHA-256 hashes) into concise, actionable Chinese messages.
func friendlyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "已取消操作"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "操作超时，请稍后重试"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "找不到相关文件，可能已被移动或删除"
	}
	if errors.Is(err, os.ErrPermission) {
		return "没有访问或保存该文件的权限"
	}

	raw := strings.Join(strings.Fields(strings.TrimSpace(err.Error())), " ")
	if raw == "" {
		return "操作未能完成，请稍后重试"
	}
	low := strings.ToLower(raw)

	switch {
	case strings.Contains(low, "sha-256") || strings.Contains(raw, "校验失败"):
		return "安装包校验未通过，文件可能已损坏，请重新下载"
	case strings.Contains(raw, "频率受限") || strings.Contains(low, "http 403") || strings.Contains(low, "http 429"):
		return "请求过于频繁，请稍后再试"
	case strings.Contains(raw, "非 https") || strings.Contains(low, "tls") || strings.Contains(low, "x509") || strings.Contains(low, "certificate"):
		return "安全连接建立失败，请检查系统时间或网络代理设置"
	case strings.Contains(low, "database is locked") || strings.Contains(low, "database is busy") || (strings.Contains(low, "sqlite") && strings.Contains(low, "locked")):
		return "笔记数据库正在被占用，请稍后重试"
	case strings.Contains(low, "zip") && (strings.Contains(low, "not a valid") || strings.Contains(low, "unexpected") || strings.Contains(low, "corrupt")):
		return "文件不完整或不是有效的 MarginNote 笔记包，请重新导出后再试"
	case strings.Contains(low, "sqlite") || strings.Contains(low, "no such table") || strings.Contains(low, "database disk image"):
		return "笔记数据库格式异常或已损坏，请重新导出后再试"
	case strings.Contains(low, "dial tcp") || strings.Contains(low, "no such host") ||
		strings.Contains(low, "connection refused") || strings.Contains(low, "network is unreachable") ||
		strings.Contains(low, "eof") || strings.Contains(low, "timeout") || strings.Contains(low, "http "):
		return "网络连接异常，请检查网络或代理设置后重试"
	case strings.Contains(raw, "手写日志"):
		return "手写批注记录读取异常，请点击重试"
	case strings.Contains(low, "pdfkit") || strings.Contains(low, "pdfrenderer") || strings.Contains(raw, "无法渲染 PDF") || strings.Contains(raw, "PDF 渲染支持"):
		return "这一页暂时无法显示，请点击重新加载"
	case strings.Contains(low, "pending.json") || strings.Contains(raw, "超过最大大小限制") || strings.Contains(low, "mib"):
		return "更新信息异常，请稍后重新检查更新"
	case strings.Contains(raw, "更新目录") || strings.Contains(raw, "无效的 APK"):
		return "更新安装包无效，请重新下载后再试"
	case strings.Contains(raw, "无效的笔记本索引路径") || strings.Contains(raw, "无效的媒体文件名"):
		return "本地笔记索引异常，请重新导入受影响的笔记本"
	}

	// Strip appended Go/OS cause after Chinese colon if technical details follow.
	if prefix, suffix, ok := strings.Cut(raw, "："); ok {
		lowSuffix := strings.ToLower(suffix)
		if strings.Contains(lowSuffix, "open ") || strings.Contains(lowSuffix, "stat ") ||
			strings.Contains(lowSuffix, "sql") || strings.Contains(lowSuffix, "zip:") ||
			strings.Contains(lowSuffix, "no such") || strings.Contains(lowSuffix, "permission") ||
			strings.Contains(lowSuffix, "error") || strings.Contains(suffix, "/") {
			log.Printf("folio error detail: %s", raw)
			if strings.TrimSpace(prefix) != "" {
				return strings.TrimSpace(prefix) + "，请稍后重试"
			}
		}
	}

	if runes := []rune(raw); len(runes) > 90 {
		log.Printf("folio error detail: %s", raw)
		return string(runes[:90]) + "…"
	}
	return raw
}
