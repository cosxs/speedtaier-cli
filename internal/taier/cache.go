package taier

import (
	"os"
	"path/filepath"
	"strings"
)

// 节点库缓存保存下载到的加密原文。配置清单给出的 MD5 就是该文件的 MD5，
// 读取时校验，内容未变化即可省去一次下载；写到一半的文件也会因校验失败而被忽略。

func serverListCachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "speedtaier", "serverlist_encrypt.json"), nil
}

func readCachedServerList(wantMD5 string) ([]byte, bool) {
	path, err := serverListCachePath()
	if err != nil {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.EqualFold(md5hex(data), wantMD5) {
		return nil, false
	}
	return data, true
}

// writeCachedServerList 尽力写入缓存，失败不影响测速。
func writeCachedServerList(data []byte) {
	path, err := serverListCachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
