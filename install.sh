#!/bin/sh
# golder 安装脚本：检测当前操作系统 / 架构，从 GitHub Releases 下载最新的
# 预编译二进制，校验 sha256 后安装到常用的 PATH 目录。
#
# 用法：
#   curl -fsSL https://golder-cli.pages.dev/install.sh | sh
#   （镜像：https://raw.githubusercontent.com/getan/golder/master/install.sh 亦可）
#
# 可用环境变量覆盖默认行为：
#   GOLDER_VERSION   指定版本（形如 v0.2.0），默认取最新 release
#   GOLDER_INSTALL_DIR  安装目录，默认 /usr/local/bin（无写权限时回退到 ~/.local/bin）
#   GITHUB_TOKEN   可选，用于提高 GitHub API 速率限制
#   GOLDER_MIRROR  可选，覆盖镜像回退列表：空格分隔的 URL 前缀（前缀拼在原 URL 前），
#                  设为 off/none 则只用直连 GitHub
#   GOLDER_SKIP_CHECKSUM  设为 1 跳过 sha256 校验（不推荐）
set -eu

REPO="getan/golder"
BINARY="golder"

info() { printf '%s\n' "golder-install: $*" >&2; }
err()  { printf '%s\n' "golder-install: error: $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || err "缺少依赖命令: $1"; }

# 1. 检测下载器（curl 或 wget）。$DLO 负责带超时防护的单次取文件：连接超时 5s，
# 传输速度低于 1KB/s 持续 15s 即判死——大陆直连 GitHub 常见的"连上后卡死"场景
# 靠它快速切换到下一个源，而不是耗完整个下载。
if command -v curl >/dev/null 2>&1; then
	DL="curl -fsSL"
	DLO="curl -fsSL --connect-timeout 5 --speed-limit 1024 --speed-time 15 -o"
elif command -v wget >/dev/null 2>&1; then
	DL="wget -qO-"
	DLO="wget -q --timeout=20 --tries=1 -O"
else
	err "需要 curl 或 wget"
fi

# 镜像回退列表：直连 GitHub 失败后按顺序尝试的公益加速前缀（gh-proxy 约定，
# 前缀直接拼在原 URL 前）。与 Go 侧 internal/selfupdate 的默认列表保持一致。
mirrors() {
	case "${GOLDER_MIRROR:-}" in
	"") printf '%s\n' "https://ghfast.top/ https://ghproxy.net/ https://gh-proxy.com/ https://gh.zwy.one/" ;;
	off | none) : ;;
	*) printf '%s\n' "$GOLDER_MIRROR" ;;
	esac
}

# download <dest> <url>：直连优先，失败后按镜像列表依次回退。镜像只加速传输，
# 校验仍由调用方用 checksums.txt 完成（走镜像时校验和也来自镜像，属接受的取舍）。
download() {
	dest=$1
	original=$2
	sources="$original"
	for m in $(mirrors); do
		sources="$sources $m$original"
	done
	for src in $sources; do
		if $DLO "$dest" "$src"; then
			[ "$src" = "$original" ] || info "镜像下载成功: $src"
			return 0
		fi
		info "下载失败，尝试下一个源: $src"
	done
	return 1
}

need tar
need uname

# sha256 校验工具：sha256sum（coreutils）/ shasum（macOS）/ openssl，三者任选其一。
SHA256=""
if command -v sha256sum >/dev/null 2>&1; then
	SHA256="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	SHA256="shasum -a 256"
elif command -v openssl >/dev/null 2>&1; then
	SHA256="openssl dgst -sha256"
fi

# 2. 检测 OS，映射到 goreleaser 的归档命名（见 .goreleaser.yaml）。
os_raw=$(uname -s)
case "$os_raw" in
	Linux)  OS="Linux" ;;
	Darwin) OS="Darwin" ;;
	MINGW* | MSYS* | CYGWIN* | Windows_NT)
		err "Windows 请从 Releases 页面下载 .zip：https://github.com/$REPO/releases" ;;
	*) err "不支持的操作系统: $os_raw" ;;
esac

# 3. 检测架构，映射到归档命名（amd64→x86_64，386→i386，arm64 保持）。
arch_raw=$(uname -m)
case "$arch_raw" in
	x86_64 | amd64) ARCH="x86_64" ;;
	arm64 | aarch64) ARCH="arm64" ;;
	i386 | i686) ARCH="i386" ;;
	*) err "不支持的架构: $arch_raw" ;;
esac

# 4. 解析目标版本：优先 GOLDER_VERSION，否则查询最新 release 的 tag。
VERSION="${GOLDER_VERSION:-}"
api_auth=""
[ -n "${GITHUB_TOKEN:-}" ] && api_auth="-H Authorization:\ Bearer\ $GITHUB_TOKEN"
if [ -z "$VERSION" ]; then
	info "查询最新 release ..."
	# 首选站点镜像的版本接口（对大陆网络更友好），失败再回退 GitHub API。
	latest_json=$($DL "https://golder-cli.pages.dev/api/latest" 2>/dev/null) || \
		latest_json=$($DL "https://api.github.com/repos/$REPO/releases/latest" $api_auth 2>/dev/null) || \
		err "无法查询最新版本，请检查网络或用 GOLDER_VERSION 指定版本"
	# 兼容两种响应格式："tag_name"（GitHub API）与 "tag"（站点 /api/latest）。
	# 用 grep -oE + cut 而非 sed，避开 GNU/BSD sed 的方言差异。
	VERSION=$(printf '%s' "$latest_json" | tr -d ' \n' | grep -oE '"tag(_name)?":"[^"]*"' | head -n1 | cut -d'"' -f4)
	[ -n "$VERSION" ] || err "无法解析最新版本号，请用 GOLDER_VERSION 指定"
fi

# 版本号字符集白名单：只允许字母、数字与 . _ -，防止拼接出异常的 URL / 文件名。
case "$VERSION" in
	*[!A-Za-z0-9._-]* | *..*) err "版本号含非法字符: $VERSION" ;;
esac

# 归档名里的版本号不带前导 v（goreleaser 的 .Version）。
VER_NUM=$(printf '%s' "$VERSION" | sed 's/^v//')
ARCHIVE="${BINARY}_${VER_NUM}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$VERSION/$ARCHIVE"

info "版本: $VERSION"
info "平台: ${OS}/${ARCH}"
info "下载: $URL"

# 5. 下载归档到临时目录。
TMP=$(mktemp -d 2>/dev/null || mktemp -d -t golder-install)
trap 'rm -rf "$TMP"' EXIT INT TERM
download "$TMP/$ARCHIVE" "$URL" || err "下载失败: $URL（可设 GOLDER_MIRROR 指定镜像前缀）"

# 6. sha256 校验：对照同一 release 的 checksums.txt，通过后才解压。
if [ "${GOLDER_SKIP_CHECKSUM:-}" = "1" ]; then
	info "已跳过 sha256 校验（GOLDER_SKIP_CHECKSUM=1）"
else
	[ -n "$SHA256" ] || err "缺少 sha256 工具（sha256sum / shasum / openssl）；确需跳过请设 GOLDER_SKIP_CHECKSUM=1"
	CHECKSUMS_URL="https://github.com/$REPO/releases/download/$VERSION/checksums.txt"
	download "$TMP/checksums.txt" "$CHECKSUMS_URL" || \
		err "无法下载 checksums.txt（可设 GOLDER_SKIP_CHECKSUM=1 跳过校验）"
	expected=$(awk -v f="$ARCHIVE" '{ n=$2; sub(/^\*/, "", n) } n == f { print $1; exit }' "$TMP/checksums.txt")
	[ -n "$expected" ] || err "checksums.txt 中缺少 $ARCHIVE 的记录"
	# 三种工具的输出格式不同（sha256sum/shasum 的哈希在首列，openssl 在末列），
	# 统一提取行内 64 位十六进制字段，避免依赖列位置。
	actual=$($SHA256 "$TMP/$ARCHIVE" | awk '{ for (i = 1; i <= NF; i++) if (length($i) == 64) { print $i; exit } }')
	if [ "$actual" != "$expected" ]; then
		# ${expected} 的花括号是必须的：全角逗号紧跟 $var 时，bash 3.2 会把多字节
		# 字节吸进变量名，在 set -u 下直接报 unbound variable 并掩盖本错误。
		err "sha256 校验失败：期望 ${expected}，实际 ${actual} —— 下载可能被篡改，已中止安装"
	fi
	info "sha256 校验通过"
fi

# 7. 解压到临时目录。
tar -xzf "$TMP/$ARCHIVE" -C "$TMP" || err "解压失败: $ARCHIVE"
[ -f "$TMP/$BINARY" ] || err "归档中未找到二进制 $BINARY"
chmod +x "$TMP/$BINARY"

# 8. 选择安装目录：GOLDER_INSTALL_DIR > /usr/local/bin > ~/.local/bin。
DIR="${GOLDER_INSTALL_DIR:-}"
if [ -z "$DIR" ]; then
	if [ -w /usr/local/bin ] 2>/dev/null; then
		DIR="/usr/local/bin"
	elif [ "$(id -u)" = "0" ]; then
		DIR="/usr/local/bin"
	else
		DIR="$HOME/.local/bin"
	fi
fi
mkdir -p "$DIR" 2>/dev/null || err "无法创建安装目录: $DIR"

# 9. 安装。若目录不可写但可 sudo，尝试用 sudo。
DEST="$DIR/$BINARY"
if [ -w "$DIR" ]; then
	mv "$TMP/$BINARY" "$DEST"
elif command -v sudo >/dev/null 2>&1; then
	info "$DIR 需要提升权限，使用 sudo 安装 ..."
	sudo mv "$TMP/$BINARY" "$DEST"
else
	err "$DIR 不可写且无 sudo，请设置 GOLDER_INSTALL_DIR 指向可写目录"
fi

info "已安装: $DEST"

# 10. 提示 PATH 是否包含安装目录。
case ":$PATH:" in
	*":$DIR:"*) : ;;
	*) info "注意: $DIR 不在 PATH 中，请将其加入 PATH，例如：" >&2
	   info "  echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.profile" >&2 ;;
esac

"$DEST" --version || true
