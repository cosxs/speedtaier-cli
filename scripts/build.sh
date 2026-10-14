#!/usr/bin/env bash
# 构建发布包：多平台静态二进制、压缩包与 sha256 校验文件，输出到 dist/。
# 版本号取自 VERSION 环境变量，否则取 git tag(git describe)，注入到二进制中。
# 用法: VERSION=v0.6.0 scripts/build.sh
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X github.com/cosxs/speedtaier-cli/internal/cli.version=${VERSION}"
PLATFORMS=(
  darwin/amd64 darwin/arm64
  linux/amd64 linux/arm64 linux/armv7 linux/386 linux/mips linux/mipsle
  windows/amd64
)

sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

rm -rf dist
mkdir -p dist
echo "构建 speedtaier ${VERSION}($(go version | awk '{print $3}'))"

for platform in "${PLATFORMS[@]}"; do
  goos="${platform%/*}" goarch="${platform#*/}" goarm="" gomips=""
  case "$goarch" in
    armv7) goarch=arm goarm=7 ;;
    # 软浮点：兼容没有浮点单元的路由器芯片(OpenWrt 的 MIPS 设备多数如此)
    mips | mipsle) gomips=softfloat ;;
  esac
  name="speedtaier-${goos}-${platform#*/}"
  stage="dist/${name}"
  bin="speedtaier"
  [[ "$goos" == windows ]] && bin+=".exe"

  mkdir -p "$stage"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" GOMIPS="$gomips" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${stage}/${bin}" ./cmd/speedtaier
  cp README.md "$stage/"
  [[ -f LICENSE ]] && cp LICENSE "$stage/"

  if [[ "$goos" == windows ]]; then
    (cd dist && zip -qr "${name}.zip" "$name")
  else
    # 不打包 macOS 扩展属性，避免其他系统的 tar 解压时告警。
    COPYFILE_DISABLE=1 tar --no-xattrs -czf "dist/${name}.tar.gz" -C dist "$name"
  fi
  rm -rf "$stage"
  echo "  ${platform}"
done

(cd dist && sha256 ./*.tar.gz ./*.zip | sed 's#\./##' > checksums-sha256.txt)
echo "产物:"
ls -lh dist | awk 'NR>1 {printf "  %-36s %s\n", $9, $5}'
