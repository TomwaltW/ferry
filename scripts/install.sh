#!/bin/sh
# Ferry installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/TomwaltW/ferry/main/scripts/install.sh | sh
#
# Environment:
#   FERRY_VERSION        tag to install, e.g. v0.1.0 (default: latest release)
#   FERRY_INSTALL_DIR    target directory (default: ~/.local/bin, else /usr/local/bin)
#   FERRY_GITHUB_PROXY   prefix put in front of every download URL, e.g. https://ghfast.top/
#   FERRY_REPO           owner/name to download from (default: TomwaltW/ferry)
set -eu

REPO="${FERRY_REPO:-TomwaltW/ferry}"
BIN_NAME="ferry"
PROXY="${FERRY_GITHUB_PROXY:-}"
WORK_DIR=""

# ---------------------------------------------------------------- messages --
# Chinese when LANG starts with zh, English otherwise.
case "${LANG:-}" in
zh*) ZH=1 ;;
*) ZH=0 ;;
esac

say() { # say <english> <chinese>
	if [ "$ZH" -eq 1 ]; then
		printf '%s\n' "$2"
	else
		printf '%s\n' "$1"
	fi
}

die() { # die <english> <chinese>
	if [ "$ZH" -eq 1 ]; then
		printf 'ferry: %s\n' "$2" >&2
	else
		printf 'ferry: %s\n' "$1" >&2
	fi
	exit 1
}

cleanup() {
	if [ -n "$WORK_DIR" ] && [ -d "$WORK_DIR" ]; then
		rm -rf "$WORK_DIR"
	fi
}
trap cleanup EXIT INT TERM

# ----------------------------------------------------------------- helpers --
url_with_proxy() { # url_with_proxy <url>
	if [ -z "$PROXY" ]; then
		printf '%s\n' "$1"
		return
	fi
	# Accept the prefix with or without a trailing slash.
	prefix="${PROXY%/}"
	printf '%s/%s\n' "$prefix" "$1"
}

fetch_to_stdout() { # fetch_to_stdout <url>
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO- "$1"
	else
		die "curl or wget is required" "需要 curl 或 wget"
	fi
}

fetch_to_file() { # fetch_to_file <url> <path>
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		die "curl or wget is required" "需要 curl 或 wget"
	fi
}

sha256_of() { # sha256_of <path>
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	else
		die "sha256sum, shasum or openssl is required" "需要 sha256sum、shasum 或 openssl"
	fi
}

# ------------------------------------------------------- platform detection --
detect_platform() {
	uname_s="$(uname -s)"
	case "$uname_s" in
	Darwin) GOOS=darwin ;;
	Linux) GOOS=linux ;;
	MINGW* | MSYS* | CYGWIN*) GOOS=windows ;;
	*) die "unsupported operating system: $uname_s" "不支持的操作系统：$uname_s" ;;
	esac

	uname_m="$(uname -m)"
	case "$uname_m" in
	x86_64 | amd64) GOARCH=amd64 ;;
	arm64 | aarch64) GOARCH=arm64 ;;
	*) die "unsupported architecture: $uname_m" "不支持的处理器架构：$uname_m" ;;
	esac

	if [ "$GOOS" = windows ]; then
		ARCHIVE_EXT=zip
		BIN_FILE="${BIN_NAME}.exe"
	else
		ARCHIVE_EXT=tar.gz
		BIN_FILE="$BIN_NAME"
	fi
}

# tag_from_redirect prints the tag that the releases/latest page redirects to,
# or nothing when that page cannot be reached.
tag_from_redirect() { # tag_from_redirect <url>
	final=""
	if command -v curl >/dev/null 2>&1; then
		final="$(curl -fsSL -o /dev/null -w '%{url_effective}' "$1" 2>/dev/null || true)"
	elif command -v wget >/dev/null 2>&1; then
		final="$(wget -qS --spider "$1" 2>&1 |
			sed -n 's/^[[:space:]]*Location:[[:space:]]*\([^[:space:]]*\).*/\1/p' | tail -n 1)"
	fi
	printf '%s\n' "$final" | sed -n 's|.*/releases/tag/\([^/?]*\)$|\1|p' | head -n 1
}

# Both lookups go through the mirror prefix. api.github.com is a different host
# from github.com and is usually the first one a restricted network drops, so
# the redirect on the releases/latest page -- an ordinary github.com URL that
# every mirror covers -- is tried first and the API is only the fallback.
resolve_version() {
	if [ -n "${FERRY_VERSION:-}" ]; then
		TAG="$FERRY_VERSION"
		return
	fi
	latest_url="$(url_with_proxy "https://github.com/${REPO}/releases/latest")"
	TAG="$(tag_from_redirect "$latest_url")"
	if [ -z "$TAG" ]; then
		api_url="$(url_with_proxy "https://api.github.com/repos/${REPO}/releases/latest")"
		body="$(fetch_to_stdout "$api_url" 2>/dev/null || true)"
		TAG="$(printf '%s\n' "$body" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
	fi
	if [ -z "$TAG" ]; then
		die "cannot resolve the latest release; set FERRY_VERSION=vX.Y.Z and retry" \
			"无法获取最新版本号；请设置 FERRY_VERSION=vX.Y.Z 后重试"
	fi
}

# The archive name carries the version without its leading "v".
archive_name() {
	version="${TAG#v}"
	printf '%s_%s_%s_%s.%s\n' "$BIN_NAME" "$version" "$GOOS" "$GOARCH" "$ARCHIVE_EXT"
}

pick_install_dir() {
	if [ -n "${FERRY_INSTALL_DIR:-}" ]; then
		INSTALL_DIR="$FERRY_INSTALL_DIR"
		return
	fi
	local_bin="${HOME:-}/.local/bin"
	if [ -n "${HOME:-}" ]; then
		if [ -d "$local_bin" ]; then
			if [ -w "$local_bin" ]; then
				INSTALL_DIR="$local_bin"
				return
			fi
		elif mkdir -p "$local_bin" 2>/dev/null; then
			INSTALL_DIR="$local_bin"
			return
		fi
	fi
	INSTALL_DIR="/usr/local/bin"
}

install_binary() { # install_binary <source path>
	src="$1"
	dest="${INSTALL_DIR}/${BIN_FILE}"
	chmod +x "$src"
	if mkdir -p "$INSTALL_DIR" 2>/dev/null && [ -w "$INSTALL_DIR" ]; then
		mv -f "$src" "$dest"
	elif command -v sudo >/dev/null 2>&1; then
		say "installing into ${INSTALL_DIR} needs sudo" "写入 ${INSTALL_DIR} 需要 sudo 授权"
		sudo mkdir -p "$INSTALL_DIR"
		sudo mv -f "$src" "$dest"
	else
		die "cannot write to ${INSTALL_DIR}; set FERRY_INSTALL_DIR to a writable directory" \
			"无法写入 ${INSTALL_DIR}；请把 FERRY_INSTALL_DIR 设为一个可写目录"
	fi
	INSTALLED_PATH="$dest"
}

warn_if_not_on_path() {
	case ":${PATH}:" in
	*":${INSTALL_DIR}:"*) ;;
	*)
		say "${INSTALL_DIR} is not on your PATH; add it in your shell profile:" \
			"${INSTALL_DIR} 不在 PATH 里；请在 shell 配置中加上："
		printf '  export PATH="%s:$PATH"\n' "$INSTALL_DIR"
		;;
	esac
}

# -------------------------------------------------------------------- main --
main() {
	detect_platform
	resolve_version

	archive="$(archive_name)"
	base_url="https://github.com/${REPO}/releases/download/${TAG}"
	archive_url="$(url_with_proxy "${base_url}/${archive}")"
	checksums_url="$(url_with_proxy "${base_url}/checksums.txt")"

	say "installing ferry ${TAG} for ${GOOS}/${GOARCH}" \
		"正在安装 ferry ${TAG}（${GOOS}/${GOARCH}）"

	WORK_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t ferry)"

	say "downloading ${archive_url}" "正在下载 ${archive_url}"
	fetch_to_file "$archive_url" "${WORK_DIR}/${archive}" ||
		die "download failed: ${archive_url}" "下载失败：${archive_url}"

	say "verifying sha256" "正在校验 sha256"
	fetch_to_file "$checksums_url" "${WORK_DIR}/checksums.txt" ||
		die "cannot download checksums.txt" "无法下载 checksums.txt"

	expected="$(awk -v name="$archive" '$2 == name || $2 == "*" name {print $1}' "${WORK_DIR}/checksums.txt" | head -n 1)"
	if [ -z "$expected" ]; then
		die "${archive} is not listed in checksums.txt" "checksums.txt 里没有 ${archive} 这一项"
	fi
	actual="$(sha256_of "${WORK_DIR}/${archive}")"
	if [ "$expected" != "$actual" ]; then
		die "checksum mismatch: expected ${expected}, got ${actual}" \
			"校验和不匹配：期望 ${expected}，实际 ${actual}"
	fi

	if [ "$ARCHIVE_EXT" = zip ]; then
		command -v unzip >/dev/null 2>&1 || die "unzip is required" "需要 unzip"
		unzip -q -o "${WORK_DIR}/${archive}" -d "${WORK_DIR}/unpacked"
	else
		mkdir -p "${WORK_DIR}/unpacked"
		tar -xzf "${WORK_DIR}/${archive}" -C "${WORK_DIR}/unpacked"
	fi

	if [ ! -f "${WORK_DIR}/unpacked/${BIN_FILE}" ]; then
		die "${BIN_FILE} not found inside ${archive}" "${archive} 里没有找到 ${BIN_FILE}"
	fi

	pick_install_dir
	install_binary "${WORK_DIR}/unpacked/${BIN_FILE}"

	say "installed ${INSTALLED_PATH}" "已安装到 ${INSTALLED_PATH}"
	warn_if_not_on_path
	say "run 'ferry --help' to get started" "运行 ferry --help 开始使用"
}

main "$@"
