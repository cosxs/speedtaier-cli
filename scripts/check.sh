#!/usr/bin/env bash
# 本地与 CI 共用的质量检查。工具版本由 go.mod 的 tool 指令固定，无需全局安装。
# 用法: scripts/check.sh [--quick]   --quick 跳过竞态检测、漏洞扫描与交叉编译
set -euo pipefail
cd "$(dirname "$0")/.."

quick=false
[[ "${1:-}" == "--quick" ]] && quick=true

step() { printf '\n==> %s\n' "$1"; }
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

step "gofmt"
unformatted="$(gofmt -l .)"
if [[ -n "$unformatted" ]]; then
  echo "以下文件未格式化(运行 gofmt -w .):"
  echo "$unformatted"
  exit 1
fi

step "go vet"
go vet ./...

step "go fix(现代化写法检查)"
fixes="$(go fix -diff ./... || true)"
if [[ -n "$fixes" ]]; then
  echo "$fixes"
  echo "存在可现代化的写法，运行 go fix ./... 应用"
  exit 1
fi

step "staticcheck"
go tool staticcheck -checks=all,-ST1000,-ST1003 ./...

step "shellcheck"
# 以能否运行判断是否可用：版本管理器的占位命令存在但未配置版本时也会失败。
if shellcheck --version >/dev/null 2>&1; then
  shellcheck -s sh scripts/install.sh
  shellcheck -S warning scripts/check.sh scripts/build.sh
else
  echo "  未安装 shellcheck，跳过"
fi

step "go test"
go test -count=1 ./...

if ! $quick; then
  # 竞态检测在 Windows 上需要 cgo 工具链，跳过。
  if [[ "$(go env GOOS)" != "windows" ]]; then
    step "go test -race"
    go test -race -count=1 ./...
  fi

  step "govulncheck"
  go tool govulncheck ./...

  step "交叉编译"
  for platform in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do
    GOOS="${platform%/*}" GOARCH="${platform#*/}" CGO_ENABLED=0 go build -o "$tmp/cross" ./cmd/speedtaier && echo "  $platform"
  done
fi

step "CLI 冒烟测试"
bin="$tmp/speedtaier$(go env GOEXE)"
go build -o "$bin" ./cmd/speedtaier
"$bin" --help >/dev/null
"$bin" version
"$bin" --threads 0 >/dev/null 2>&1 && { echo "非法参数应当失败"; exit 1; }
echo "  参数校验 OK"

printf '\n全部检查通过\n'
