package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	binName    = "speedtaier"
	maxArchive = 64 << 20 // 发布包与解压后程序的大小上限，防止异常数据占满内存
)

// AssetName 返回当前平台的发布包文件名，如 speedtaier-linux-armv7.tar.gz；goarm 仅对 arm 有意义。
func AssetName(goos, goarch, goarm string) (string, error) {
	arch := goarch
	if goarch == "arm" {
		if goarm != "" && goarm != "7" {
			return "", fmt.Errorf("不支持 GOARM=%s(发布包只有 armv7)", goarm)
		}
		arch = "armv7"
	}
	switch goos + "/" + arch {
	case "darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "linux/armv7", "linux/386":
		return fmt.Sprintf("%s-%s-%s.tar.gz", binName, goos, arch), nil
	case "windows/amd64":
		return fmt.Sprintf("%s-%s-%s.zip", binName, goos, arch), nil
	}
	return "", fmt.Errorf("没有适用于 %s/%s 的发布包", goos, goarch)
}

// Install 下载标签 tag 的发布包 asset，校验 sha256 后替换程序文件 exe。
// verify 在替换前检查新程序能否运行(可为 nil)，失败则保留原程序。
func (s Source) Install(ctx context.Context, tag, asset, exe string, verify func(path string) error) error {
	base := s.download() + "/" + Repo + "/releases/download/" + tag

	sums, err := s.get(ctx, base+"/checksums-sha256.txt", 1<<20)
	if err != nil {
		return fmt.Errorf("下载校验文件失败: %w", err)
	}
	want, ok := checksumOf(string(sums), asset)
	if !ok {
		return fmt.Errorf("校验文件中没有 %s", asset)
	}
	archive, err := s.get(ctx, base+"/"+asset, maxArchive)
	if err != nil {
		return fmt.Errorf("下载 %s 失败: %w", asset, err)
	}
	if sum := sha256.Sum256(archive); !strings.EqualFold(hex.EncodeToString(sum[:]), want) {
		return fmt.Errorf("校验失败：%s 可能下载不完整或被篡改", asset)
	}
	program, err := extractProgram(archive, asset)
	if err != nil {
		return err
	}
	return replaceProgram(exe, program, verify)
}

// checksumOf 在 sha256sum 格式的校验文件("<hash>  <文件名>")中查找 asset 的摘要。
func checksumOf(sums, asset string) (string, bool) {
	for line := range strings.SplitSeq(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], true
		}
	}
	return "", false
}

// extractProgram 从 tar.gz 或 zip 发布包中取出程序文件。只读取内容，不使用包内路径，避免路径穿越。
func extractProgram(archive []byte, asset string) ([]byte, error) {
	isProgram := func(name string) bool {
		base := path.Base(name)
		return base == binName || base == binName+".exe"
	}
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("解压失败: %w", err)
		}
		for _, f := range zr.File {
			if f.FileInfo().Mode().IsRegular() && isProgram(f.Name) {
				rc, err := f.Open()
				if err != nil {
					return nil, fmt.Errorf("解压失败: %w", err)
				}
				defer rc.Close()
				return readProgram(rc)
			}
		}
		return nil, errors.New("压缩包中没有程序文件")
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("解压失败: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("压缩包中没有程序文件")
		}
		if err != nil {
			return nil, fmt.Errorf("解压失败: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && isProgram(hdr.Name) {
			return readProgram(tr)
		}
	}
}

func readProgram(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxArchive+1))
	if err != nil {
		return nil, fmt.Errorf("解压失败: %w", err)
	}
	if len(data) > maxArchive {
		return nil, errors.New("解压后的程序过大")
	}
	if len(data) == 0 {
		return nil, errors.New("压缩包中的程序文件为空")
	}
	return data, nil
}

// siblingPath 返回与 exe 同目录的临时文件路径：speedtaier → speedtaier.new，speedtaier.exe → speedtaier.new.exe
// (Windows 只会执行 .exe 扩展名的文件，校验新程序时需要运行它)。
func siblingPath(exe, tag string) string {
	ext := filepath.Ext(exe)
	return strings.TrimSuffix(exe, ext) + "." + tag + ext
}

// replaceProgram 先在同一目录写出临时文件并校验，再改名覆盖 exe。
// Unix 下可以直接覆盖正在运行的程序；Windows 不允许，需先把旧文件改名让开。
func replaceProgram(exe string, program []byte, verify func(string) error) error {
	tmp := siblingPath(exe, "new")
	if err := os.WriteFile(tmp, program, 0o755); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("%w: %s", ErrNoWrite, tmp)
		}
		return fmt.Errorf("写入 %s 失败: %w", tmp, err)
	}
	// WriteFile 不会改变已存在文件的权限位，显式设置。
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if verify != nil {
		if err := verify(tmp); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("新版本无法运行(架构不匹配?)，已保留原程序: %w", err)
		}
	}

	if runtime.GOOS == "windows" {
		old := siblingPath(exe, "old")
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("替换 %s 失败: %w", exe, err)
		}
		if err := os.Rename(tmp, exe); err != nil {
			_ = os.Rename(old, exe)
			_ = os.Remove(tmp)
			return fmt.Errorf("替换 %s 失败: %w", exe, err)
		}
		_ = os.Remove(old) // 旧程序仍在运行时删除会失败，留待下次升级清理
		return nil
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换 %s 失败: %w", exe, err)
	}
	return nil
}
