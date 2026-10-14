#!/bin/sh
# SpeedTaier CLI 一键安装：识别系统与架构，从 GitHub Releases 下载、校验并安装。
# 兼容 POSIX sh，可在 OpenWrt/iStoreOS(BusyBox ash)、Alpine、Debian/Ubuntu、macOS 上运行。
#
#   curl -fsSL https://raw.githubusercontent.com/cosxs/speedtaier-cli/main/scripts/install.sh | sh
#   wget -qO- https://raw.githubusercontent.com/cosxs/speedtaier-cli/main/scripts/install.sh | sh
#
# 可选环境变量(放在 sh 前，如 ... | VERSION=v0.6.2 sh)：
#   VERSION       安装指定版本，如 v0.6.2；默认最新版
#   INSTALL_DIR   安装目录；默认 root 为 /usr/local/bin 或 /usr/bin，普通用户为 ~/.local/bin
#   GITHUB_PROXY  GitHub 下载加速前缀，最终地址为 前缀 + 原始地址，如 https://proxy.example.com/
#   RELEASE_BASE  自定义发布包下载地址(目录)，设置后忽略 VERSION 与 GITHUB_PROXY
#   ARCH          跳过自动检测，直接指定架构：amd64、arm64、armv7、386
set -eu

REPO="cosxs/speedtaier-cli"
BIN="speedtaier"

info() { printf '==> %s\n' "$*"; }
warn() { printf '警告: %s\n' "$*" >&2; }
die() {
	printf '错误: %s\n' "$*" >&2
	exit 1
}
has() { command -v "$1" >/dev/null 2>&1; }

in_path() {
	case ":${PATH:-}:" in
	*":$1:"*) return 0 ;;
	esac
	return 1
}

download() { # 地址 输出文件
	if has curl; then
		curl -fsSL --retry 3 --connect-timeout 15 -o "$2" "$1"
	elif has wget; then
		wget -q -T 30 -O "$2" "$1"
	else
		die "需要 curl 或 wget"
	fi
}

detect_os() {
	case "$(uname -s)" in
	Linux) echo linux ;;
	Darwin) echo darwin ;;
	MINGW* | MSYS* | CYGWIN*) die "Windows 请从 https://github.com/$REPO/releases/latest 下载 zip 包" ;;
	*) die "不支持的系统: $(uname -s)" ;;
	esac
}

detect_arch() {
	if [ -n "${ARCH:-}" ]; then
		echo "$ARCH"
		return
	fi
	machine="$(uname -m)"
	case "$machine" in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	armv7* | armv8l) echo armv7 ;;
	i386 | i486 | i586 | i686) echo 386 ;;
	*) die "不支持的架构: $machine" ;;
	esac
}

sha256_of() {
	if has sha256sum; then
		sha256sum "$1" | awk '{print $1}'
	elif has shasum; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif has openssl; then
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	fi
}

choose_dir() {
	if [ -n "${INSTALL_DIR:-}" ]; then
		echo "${INSTALL_DIR%/}"
		return
	fi
	if [ "$(id -u)" = 0 ]; then
		for dir in /usr/local/bin /usr/bin; do
			if [ -d "$dir" ] && in_path "$dir"; then
				echo "$dir"
				return
			fi
		done
		echo /usr/bin
		return
	fi
	for dir in "$HOME/.local/bin" "$HOME/bin"; do
		if in_path "$dir"; then
			echo "$dir"
			return
		fi
	done
	echo "$HOME/.local/bin"
}

# free_kb 输出目录所在文件系统的可用空间(KB)。df 在设备名过长时会折行，因此取最后一行倒数第三列。
free_kb() {
	{ df -Pk "$1" 2>/dev/null || df -k "$1"; } | awk 'NR > 1 { avail = $(NF - 2) } END { print avail + 0 }'
}

main() {
	os="$(detect_os)"
	arch="$(detect_arch)"
	asset="$BIN-$os-$arch.tar.gz"

	if [ -n "${RELEASE_BASE:-}" ]; then
		base="${RELEASE_BASE%/}"
	elif [ -n "${VERSION:-}" ]; then
		version="v${VERSION#v}"
		base="${GITHUB_PROXY:-}https://github.com/$REPO/releases/download/$version"
	else
		base="${GITHUB_PROXY:-}https://github.com/$REPO/releases/latest/download"
	fi

	tmp="$(mktemp -d 2>/dev/null || mktemp -d -t "$BIN")"
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	info "平台: $os/$arch"
	info "下载 $base/$asset"
	download "$base/$asset" "$tmp/$asset" || die "下载失败；访问 GitHub 困难时可设置 GITHUB_PROXY 使用加速地址"
	download "$base/checksums-sha256.txt" "$tmp/checksums.txt" || die "下载校验文件失败"

	expected="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")"
	[ -n "$expected" ] || die "校验文件中没有 $asset"
	actual="$(sha256_of "$tmp/$asset")"
	if [ -z "$actual" ]; then
		warn "缺少 sha256sum/shasum/openssl，跳过完整性校验"
	elif [ "$actual" != "$expected" ]; then
		die "校验失败：$asset 可能下载不完整或被篡改"
	else
		info "sha256 校验通过"
	fi

	if ! tar -xzf "$tmp/$asset" -C "$tmp" 2>/dev/null; then
		gzip -dc "$tmp/$asset" | tar -xf - -C "$tmp" || die "解压失败"
	fi
	src="$tmp/$BIN-$os-$arch/$BIN"
	[ -f "$src" ] || die "压缩包中没有 $BIN"

	dir="$(choose_dir)"
	mkdir -p "$dir" || die "无法创建目录 $dir"
	[ -w "$dir" ] || die "没有 $dir 的写权限；请用 root 运行，或设置 INSTALL_DIR 安装到其他目录"
	need_kb=$(($(wc -c <"$src") / 1024 + 512))
	avail_kb="$(free_kb "$dir")"
	if [ "$avail_kb" -gt 0 ] && [ "$avail_kb" -lt "$need_kb" ]; then
		die "$dir 剩余空间 $((avail_kb / 1024)) MB，不足 $((need_kb / 1024 + 1)) MB。可安装到内存盘(重启后需重装)：INSTALL_DIR=/tmp sh"
	fi

	target="$dir/$BIN"
	old=""
	[ -x "$target" ] && old="$("$target" version 2>/dev/null || true)"
	# 先复制为临时文件再改名，避免覆盖正在运行的程序时出错。
	if ! { cp "$src" "$target.new" && chmod 755 "$target.new" && mv -f "$target.new" "$target"; }; then
		die "写入 $target 失败"
	fi

	[ -n "$old" ] && info "已替换旧版本: $old"
	info "已安装到 $target"
	"$target" version || warn "无法运行 $target，请确认架构是否匹配(当前 $arch)"

	if ! in_path "$dir"; then
		warn "$dir 不在 PATH 中，可执行: export PATH=\"$dir:\$PATH\"(写入 ~/.profile 后长期生效)"
	fi
	info "开始测速: $BIN --province <省份拼音>，如 $BIN --province jiangsu"
}

main "$@"
