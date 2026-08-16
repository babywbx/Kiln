#!/bin/sh

set -eu

REPO="babywbx/Kiln"
GITHUB="https://github.com"
MIRRORS="https://ghfast.top https://ghproxy.net https://gh-proxy.com"
PROBE_TIMEOUT=3

ACTION="install"
ASSUME_YES="${KILN_YES:-0}"
VERSION_REQ="${KILN_VERSION:-}"
VARIANT="${KILN_VARIANT:-full}"
DIR_REQ="${KILN_INSTALL_DIR:-}"
MIRROR_REQ="${KILN_MIRROR:-}"
NO_MIRROR="${KILN_NO_MIRROR:-0}"
WITH_SERVICE=0
DRY_RUN="${KILN_DRY_RUN:-0}"
FORCE_LANG="${KILN_LANG:-}"
SYSTEM_ROOT="${KILN_TEST_ROOT:-}"
SERVICE_UNIT="${SYSTEM_ROOT}/etc/systemd/system/kiln.service"
SERVICE_CONFIG_DIR="${SYSTEM_ROOT}/etc/kiln"
SERVICE_CONFIG="${SERVICE_CONFIG_DIR}/kiln.toml"
SERVICE_DATA="${SYSTEM_ROOT}/var/lib/kiln"
BAD_ARG=""
BAD_KIND=""

has_option_value() {
	[ "$#" -ge 2 ] && [ -n "$2" ] && [ "${2#-}" = "$2" ]
}

while [ $# -gt 0 ]; do
	case "$1" in
	--yes | -y) ASSUME_YES=1 ;;
	--version)
		has_option_value "$@" || { BAD_ARG="$1" && BAD_KIND="value"; }
		[ -n "$BAD_ARG" ] || { VERSION_REQ="$2" && shift; }
		;;
	--lite) VARIANT="lite" ;;
	--dir)
		has_option_value "$@" || { BAD_ARG="$1" && BAD_KIND="value"; }
		[ -n "$BAD_ARG" ] || { DIR_REQ="$2" && shift; }
		;;
	--mirror)
		has_option_value "$@" || { BAD_ARG="$1" && BAD_KIND="value"; }
		[ -n "$BAD_ARG" ] || { MIRROR_REQ="$2" && shift; }
		;;
	--no-mirror) NO_MIRROR=1 ;;
	--lang)
		has_option_value "$@" || { BAD_ARG="$1" && BAD_KIND="value"; }
		if [ -z "$BAD_ARG" ]; then
			case "$2" in
			zh | en) FORCE_LANG="$2" ;;
			*) BAD_ARG="--lang $2" && BAD_KIND="lang" ;;
			esac
			shift
		fi
		;;
	--service) WITH_SERVICE=1 ;;
	--uninstall) ACTION="remove" ;;
	--dry-run) DRY_RUN=1 ;;
	--help | -h) ACTION="help" ;;
	*) BAD_ARG="$1" && BAD_KIND="unknown" ;;
	esac
	[ -n "$BAD_ARG" ] && break
	shift
done
VERSION_INPUT="$VERSION_REQ"
VERSION_REQ="${VERSION_REQ#v}"
case "$VARIANT" in full | lite) ;; *) BAD_ARG="KILN_VARIANT=$VARIANT" && BAD_KIND="variant" ;; esac
if [ -z "$BAD_ARG" ] && [ -n "$MIRROR_REQ" ] && [ "$NO_MIRROR" = 1 ]; then
	BAD_ARG="--mirror + --no-mirror"
	BAD_KIND="mirror_conflict"
elif [ -z "$BAD_ARG" ] && [ -n "$MIRROR_REQ" ]; then
	case "$MIRROR_REQ" in
	https://?*) case "$MIRROR_REQ" in *[[:space:]]*) BAD_ARG="$MIRROR_REQ" && BAD_KIND="mirror" ;; esac ;;
	*) BAD_ARG="$MIRROR_REQ" && BAD_KIND="mirror" ;;
	esac
	while [ "${MIRROR_REQ%/}" != "$MIRROR_REQ" ]; do MIRROR_REQ="${MIRROR_REQ%/}"; done
fi

lang="en"
case "${FORCE_LANG:-${LC_ALL:-${LANG:-}}}" in
zh*) lang="zh" ;;
esac
[ "$FORCE_LANG" = "zh" ] && lang="zh"
[ "$FORCE_LANG" = "en" ] && lang="en"

if [ "$lang" = "zh" ]; then
	T_TITLE="安装程序"
	T_DRY="试运行模式：不写入任何文件"
	T_FETCH="正在获取最新版本…"
	T_PROBE="正在检测下载源…"
	T_PLAN="即将执行："
	T_VER="版本    "
	T_VARIANT="变体    "
	T_PLAT="平台    "
	T_DEST="位置    "
	T_SRC="下载源  "
	T_DEC="解码器  "
	T_SVC="服务    "
	T_LATEST="（最新）"
	T_UP_FROM="（从 v%s 升级）"
	T_DOWN_TO="（从 v%s 降级）"
	T_SWITCH_FROM="（从 %s 切换）"
	T_SAME="已安装 v%s，即为最新版本。"
	T_SRC_DIRECT="github.com（直连）"
	T_MIRROR_TAG="镜像"
	T_SRC_MIRROR_NOTE="github.com 直连不可用，已切换镜像；校验和仍从 github.com 获取。"
	T_DEC_YES="ffmpeg 已检测到（兼容引擎可用）"
	T_DEC_NO="未检测到 ffmpeg（使用原生引擎，无需安装）"
	T_SVC_ON="安装 systemd unit；配置存在时启用并启动"
	T_ACT_ASK="检测到已安装 kiln v%s，要做什么？"
	T_ACT_UP="1) 升级到 v%s（默认）"
	T_ACT_RE="1) 重新安装 v%s"
	T_ACT_SWITCH="1) 切换到 %s 变体（默认）"
	T_ACT_RM="2) 卸载"
	T_ACT_CANCEL="3) 取消"
	T_ACT_P_UP="请选择 [1-3，默认 1] "
	T_ACT_P_SAME="请选择 [1-3，默认 3] "
	T_CONFIRM="确认安装？[Y/n] "
	T_CONFIRM_UP="确认升级？[Y/n] "
	T_CONFIRM_DOWN="确认降级？[y/N] "
	T_CONFIRM_RM="确认卸载 %s？[y/N] "
	T_CANCELLED="已取消，未对系统做任何修改。"
	T_DIR_RO="安装目录不可写："
	T_DIR_TYPE="安装位置不是目录："
	T_BIN_DIR="目标二进制路径是目录："
	T_HOME_MISS="HOME 未设置，无法选择用户安装目录。"
	T_PATH_WARN="PATH 未包含 %s，执行下面这行加入："
	L_DL="下载        "
	L_VERIFY="校验 SHA256 "
	L_INSTALL="安装        "
	L_REMOVE="移除        "
	L_SERVICE="配置服务    "
	T_MATCH="匹配"
	T_MATCH_SRC="（SHA256SUMS）"
	T_DRYMARK="（试运行）"
	T_MISMATCH="与 SHA256SUMS 不匹配"
	T_FAIL_SUM_1="下载的文件校验失败，已丢弃，未对系统做任何修改。"
	T_FAIL_SUM_2="可能是网络异常或产物损坏，请重试，或前往 GitHub Releases 手动下载。"
	T_NET_FAIL="无法连接任何下载源。"
	T_NET_HINT_1="请检查网络后重试；可用 --mirror <地址> 手动指定镜像，"
	T_NET_HINT_2="或前往 https://github.com/babywbx/Kiln/releases 手动下载。"
	T_VER_FAIL="无法解析版本 %s（对应 Release 不存在）。"
	T_PLAT_WIN="Windows 请下载 zip 包并参阅 README 的 Windows 服务章节。"
	T_PLAT_UNSUP="暂不支持当前平台："
	T_LITE_LINUX="lite 变体仅提供 Linux 构建。"
	T_TOOL_MISS="缺少依赖工具："
	T_TOOL_HINT="请先安装，例如："
	T_SVC_LINUX="--service 仅支持 Linux + systemd。"
	T_SVC_ROOT="--service 需要 root 权限，请改用 sudo 运行本脚本。"
	T_SVC_DIR="--service 要求绝对安装路径，且不能位于 home 或临时目录。"
	T_SVC_FAIL="systemd 服务配置失败。"
	T_RM_SVC_ROOT="检测到 systemd 服务，需 root 移除：sudo systemctl disable --now kiln && sudo rm /etc/systemd/system/kiln.service && sudo systemctl daemon-reload"
	T_SVC_CFG_MISS="未找到 /etc/kiln/kiln.toml，unit 已安装但未启用；创建配置后执行 systemctl enable --now kiln。"
	T_RM_NONE="未检测到已安装的 kiln。"
	T_RUN_FAIL="安装后的二进制无法执行，已保留现场。"
	T_RUN_FAIL_HINT="可能原因：目标目录挂载为 noexec，或下载了错误架构的构建。"
	T_ID_FAIL="下载的二进制版本或变体与请求不一致。"
	T_INSTALL_FAIL="无法写入安装文件。"
	T_RM_SVC="已停止并移除 systemd 服务（保留 /etc/kiln 配置与数据）。"
	T_DONE="✓ 安装完成"
	T_DONE_UP="✓ 升级完成"
	T_DONE_RM="✓ 卸载完成"
	T_RM_HINT="重新安装可再次运行本安装脚本。"
	C_BIN="二进制"
	C_RUN="启动　"
	C_UP="升级　"
	C_RM="卸载　"
	C_DOC="文档　"
	C_STATUS="状态　"
	C_LOG="日志　"
	C_ENABLE="启用　"
	C_RUN_V="kiln -config kiln.toml"
	C_UP_V="重新运行本安装脚本"
	C_RM_V="重新运行本安装脚本并附加 --uninstall --dir"
	T_INT="已取消。"
	T_BAD_UNKNOWN="未知参数："
	T_BAD_VALUE="参数缺少值："
	T_BAD_LANG="--lang 仅接受 zh 或 en，收到："
	T_BAD_VARIANT="变体仅接受 full 或 lite，收到："
	T_BAD_VERSION="版本格式无效："
	T_BAD_MIRROR="--mirror 仅接受 HTTPS 地址，收到："
	T_BAD_MIRROR_CONFLICT="--mirror 与 --no-mirror 不能同时使用。"
	T_RM_SVC_FAIL="无法停止并移除 systemd 服务，二进制未删除。"
	T_BAD_HINT="用 --help 查看用法。"
	P_L="（"
	P_R="）"
else
	T_TITLE="installer"
	T_DRY="dry-run mode: nothing will be written"
	T_FETCH="resolving latest version…"
	T_PROBE="probing download sources…"
	T_PLAN="About to run:"
	T_VER="Version   "
	T_VARIANT="Variant   "
	T_PLAT="Platform  "
	T_DEST="Location  "
	T_SRC="Source    "
	T_DEC="Decoder   "
	T_SVC="Service   "
	T_LATEST=" (latest)"
	T_UP_FROM=" (upgrading from v%s)"
	T_DOWN_TO=" (downgrading from v%s)"
	T_SWITCH_FROM=" (switching from %s)"
	T_SAME="kiln v%s is already the latest installed version."
	T_SRC_DIRECT="github.com (direct)"
	T_MIRROR_TAG="mirror"
	T_SRC_MIRROR_NOTE="github.com unreachable, using mirror; checksums still come from github.com."
	T_DEC_YES="ffmpeg found (compat engine available)"
	T_DEC_NO="ffmpeg not found (native engine, nothing to install)"
	T_SVC_ON="install the systemd unit; enable and start when configured"
	T_ACT_ASK="kiln v%s is already installed. What next?"
	T_ACT_UP="1) Upgrade to v%s (default)"
	T_ACT_RE="1) Reinstall v%s"
	T_ACT_SWITCH="1) Switch to the %s variant (default)"
	T_ACT_RM="2) Uninstall"
	T_ACT_CANCEL="3) Cancel"
	T_ACT_P_UP="Select [1-3, default 1] "
	T_ACT_P_SAME="Select [1-3, default 3] "
	T_CONFIRM="Proceed with install? [Y/n] "
	T_CONFIRM_UP="Proceed with upgrade? [Y/n] "
	T_CONFIRM_DOWN="Proceed with downgrade? [y/N] "
	T_CONFIRM_RM="Remove %s? [y/N] "
	T_CANCELLED="Cancelled. Nothing was changed."
	T_DIR_RO="Install directory is not writable: "
	T_DIR_TYPE="Install location is not a directory: "
	T_BIN_DIR="Target binary path is a directory: "
	T_HOME_MISS="HOME is unset, so a user install directory cannot be selected."
	T_PATH_WARN="%s is not in PATH. Add it with:"
	L_DL="download    "
	L_VERIFY="verify      "
	L_INSTALL="install     "
	L_REMOVE="remove      "
	L_SERVICE="service     "
	T_MATCH="ok"
	T_MATCH_SRC=" (SHA256SUMS)"
	T_DRYMARK=" (dry-run)"
	T_MISMATCH="does not match SHA256SUMS"
	T_FAIL_SUM_1="Downloaded file failed verification and was discarded. Nothing was changed."
	T_FAIL_SUM_2="Possible network issue or corrupted artifact. Retry, or download manually from GitHub Releases."
	T_NET_FAIL="Could not reach any download source."
	T_NET_HINT_1="Check your network and retry; use --mirror <base> to set one manually,"
	T_NET_HINT_2="or download manually from https://github.com/babywbx/Kiln/releases."
	T_VER_FAIL="Could not resolve version %s (no matching release)."
	T_PLAT_WIN="On Windows, download the zip and see the Windows service section in the README."
	T_PLAT_UNSUP="Unsupported platform: "
	T_LITE_LINUX="The lite variant is only built for Linux."
	T_TOOL_MISS="Missing required tools: "
	T_TOOL_HINT="Install them first, for example:"
	T_SVC_LINUX="--service requires Linux with systemd."
	T_SVC_ROOT="--service needs root. Re-run this script with sudo."
	T_SVC_DIR="--service requires an absolute install path outside home and temporary directories."
	T_SVC_FAIL="Could not configure the systemd service."
	T_RM_SVC_ROOT="systemd service found; remove it as root: sudo systemctl disable --now kiln && sudo rm /etc/systemd/system/kiln.service && sudo systemctl daemon-reload"
	T_SVC_CFG_MISS="/etc/kiln/kiln.toml not found; the unit was installed but left disabled. Create the config, then run systemctl enable --now kiln."
	T_RM_NONE="No installed kiln found."
	T_RUN_FAIL="The installed binary failed to execute."
	T_RUN_FAIL_HINT="Likely causes: target directory mounted noexec, or a wrong-architecture build."
	T_ID_FAIL="The downloaded binary version or variant does not match the request."
	T_INSTALL_FAIL="Could not write the installation files."
	T_RM_SVC="Stopped and removed the systemd service (kept /etc/kiln config and data)."
	T_DONE="✓ Installed"
	T_DONE_UP="✓ Upgraded"
	T_DONE_RM="✓ Uninstalled"
	T_RM_HINT="Run this script again to reinstall."
	C_BIN="Binary  "
	C_RUN="Run     "
	C_UP="Upgrade "
	C_RM="Remove  "
	C_DOC="Docs    "
	C_STATUS="Status  "
	C_LOG="Logs    "
	C_ENABLE="Enable  "
	C_RUN_V="kiln -config kiln.toml"
	C_UP_V="re-run this install script"
	C_RM_V="re-run this install script with --uninstall --dir"
	T_INT="Cancelled."
	T_BAD_UNKNOWN="unknown option: "
	T_BAD_VALUE="option needs a value: "
	T_BAD_LANG="--lang accepts zh or en, got: "
	T_BAD_VARIANT="variant accepts full or lite, got: "
	T_BAD_VERSION="invalid version: "
	T_BAD_MIRROR="--mirror requires an HTTPS URL, got: "
	T_BAD_MIRROR_CONFLICT="--mirror and --no-mirror cannot be used together."
	T_RM_SVC_FAIL="Could not stop and remove the systemd service; the binary was kept."
	T_BAD_HINT="See --help for usage."
	P_L=" ("
	P_R=")"
fi

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	BOLD="$(printf '\033[1m')"
	DIM="$(printf '\033[2m')"
	CYAN="$(printf '\033[36m')"
	GREEN="$(printf '\033[32m')"
	YELLOW="$(printf '\033[33m')"
	RED="$(printf '\033[31m')"
	RESET="$(printf '\033[0m')"
	CURSOR_HIDE="$(printf '\033[?25l')"
	CURSOR_SHOW="$(printf '\033[?25h')"
	CLEAR_LINE="$(printf '\r\033[K')"
	ANIMATE=1
else
	BOLD=""
	DIM=""
	CYAN=""
	GREEN=""
	YELLOW=""
	RED=""
	RESET=""
	CURSOR_HIDE=""
	CURSOR_SHOW=""
	CLEAR_LINE=""
	ANIMATE=0
fi

usage() {
	cat <<EOF
Kiln installer

  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sh
  curl -fsSL .../install.sh | sh -s -- --yes --lite

Options:
  --yes, -y          non-interactive, accept all defaults
  --version <v>      pin a version (default: latest)
  --lite             install the lite variant (Linux only)
  --dir <path>       install directory (explicit path: no fallback)
  --mirror <base>    use a GitHub proxy mirror, skip probing
  --no-mirror        direct connection only
  --lang zh|en       force output language
  --service          install systemd unit; enable when configured (Linux, root)
  --uninstall        remove installed binary (and service if present)
  --dry-run          show and simulate every step, write nothing
  --help, -h         this help

Environment: KILN_YES KILN_VERSION KILN_VARIANT KILN_INSTALL_DIR
             KILN_MIRROR KILN_NO_MIRROR KILN_LANG KILN_DRY_RUN
EOF
}

if [ "$ACTION" = "help" ]; then
	usage
	exit 0
fi

if [ -n "$BAD_ARG" ]; then
	case "$BAD_KIND" in
	value) printf '  %s✗ %s%s%s\n' "$RED" "$T_BAD_VALUE" "$BAD_ARG" "$RESET" >&2 ;;
		lang) printf '  %s✗ %s%s%s\n' "$RED" "$T_BAD_LANG" "${BAD_ARG#--lang }" "$RESET" >&2 ;;
		variant) printf '  %s✗ %s%s%s\n' "$RED" "$T_BAD_VARIANT" "${BAD_ARG#KILN_VARIANT=}" "$RESET" >&2 ;;
		mirror) printf '  %s✗ %s%s%s\n' "$RED" "$T_BAD_MIRROR" "$BAD_ARG" "$RESET" >&2 ;;
		mirror_conflict) printf '  %s✗ %s%s%s\n' "$RED" "$T_BAD_MIRROR_CONFLICT" "" "$RESET" >&2 ;;
		*) printf '  %s✗ %s%s%s\n' "$RED" "$T_BAD_UNKNOWN" "$BAD_ARG" "$RESET" >&2 ;;
	esac
	printf '  %s%s%s\n' "$DIM" "$T_BAD_HINT" "$RESET" >&2
	exit 1
fi

WORK=""
STAGED=""
BINARY_BACKUP=""
UNIT_BACKUP=""
SERVICE_ROLLBACK=0
SERVICE_WAS_ACTIVE=0
SERVICE_WAS_ENABLED=0
cleanup() {
	printf '%s' "$CURSOR_SHOW"
	if [ -n "$STAGED" ] && [ -e "$STAGED" ]; then rm -f "$STAGED"; fi
	if [ "$SERVICE_ROLLBACK" = 1 ]; then
		if [ -n "$BINARY_BACKUP" ] && [ -e "$BINARY_BACKUP" ]; then
			mv -f "$BINARY_BACKUP" "$target/kiln" 2>/dev/null || true
		else
			rm -f "$target/kiln" 2>/dev/null || true
		fi
		if [ -n "$UNIT_BACKUP" ] && [ -e "$UNIT_BACKUP" ]; then
			mv -f "$UNIT_BACKUP" "$SERVICE_UNIT" 2>/dev/null || true
		else
			rm -f "$SERVICE_UNIT" 2>/dev/null || true
		fi
		if command -v systemctl >/dev/null 2>&1; then
			systemctl daemon-reload >/dev/null 2>&1 || true
			if [ "$SERVICE_WAS_ENABLED" = 1 ]; then
				systemctl enable kiln >/dev/null 2>&1 || true
			else
				systemctl disable kiln >/dev/null 2>&1 || true
			fi
			if [ "$SERVICE_WAS_ACTIVE" = 1 ]; then
				systemctl restart kiln >/dev/null 2>&1 || true
			else
				systemctl stop kiln >/dev/null 2>&1 || true
			fi
		fi
	else
		[ -z "$BINARY_BACKUP" ] || rm -f "$BINARY_BACKUP"
		[ -z "$UNIT_BACKUP" ] || rm -f "$UNIT_BACKUP"
	fi
	if [ -n "$WORK" ]; then rm -rf "$WORK"; fi
}
trap cleanup EXIT
trap 'printf "%s%s✗ %s%s\n" "$CLEAR_LINE" "$RED" "$T_INT" "$RESET"; exit 130' INT TERM

die() {
	code="$1"
	shift
	printf '%s  %s✗ %s%s\n' "$CLEAR_LINE" "$RED" "$1" "$RESET" >&2
	shift
	for line in "$@"; do
		printf '  %s%s%s\n' "$DIM" "$line" "$RESET" >&2
	done
	exit "$code"
}

spin_while() {
	label="$1"
	pid="$2"
	if [ "$ANIMATE" != 1 ]; then
		wait "$pid" 2>/dev/null || true
		return 0
	fi
	while kill -0 "$pid" 2>/dev/null; do
		for frame in ⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏; do
			kill -0 "$pid" 2>/dev/null || break
			printf '%s  %s%s%s %s%s%s' "$CLEAR_LINE" "$CYAN" "$frame" "$RESET" "$DIM" "$label" "$RESET"
			sleep 0.04
		done
	done
	wait "$pid" 2>/dev/null || true
	printf '%s' "$CLEAR_LINE"
}

step_done() {
	printf '%s  %s[%s]%s %s%s✓%s %s\n' "$CLEAR_LINE" "$DIM" "$1" "$RESET" "$2" "$GREEN" "$RESET" "$3"
}

warn() {
	printf '  %s! %s%s\n' "$YELLOW" "$1" "$RESET"
}

ask() {
	prompt="$1"
	default="$2"
	answer=""
	if [ "$ASSUME_YES" = 1 ]; then
		printf '%s' "$default"
		return 0
	fi
	if [ -t 0 ]; then
		printf '  %s' "$prompt" >&2
		IFS= read -r answer || answer=""
	elif [ -r /dev/tty ] && [ -w /dev/tty ]; then
		printf '  %s' "$prompt" >/dev/tty
		IFS= read -r answer </dev/tty || answer=""
	fi
	answer="$(printf '%s' "$answer" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
	if [ -z "$answer" ]; then
		printf '%s' "$default"
	else
		printf '%s' "$answer"
	fi
}

fetch() {
	if [ "$DLD" = "curl" ]; then
		curl -fsSL --proto '=https' --tlsv1.2 --retry 2 --connect-timeout 10 --max-time "${3:-60}" -o "$2" "$1"
	else
		wget -q -T "${3:-60}" -O "$2" "$1"
	fi
}

probe_ok() {
	if [ "$DLD" = "curl" ]; then
		curl -fsSIL --proto '=https' --tlsv1.2 --max-time "$PROBE_TIMEOUT" -o /dev/null "$1"
	else
		wget -q --spider -T "$PROBE_TIMEOUT" "$1"
	fi
}

valid_version() {
	case "$1" in "" | *[!0-9A-Za-z.-]*) return 1 ;; esac
	[ "${#1}" -le 123 ] || return 1
	printf '%s\n' "$1" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$' || return 1
	case "$1" in
	*-*)
		prerelease="${1#*-}"
		! printf '%s\n' "$prerelease" | grep -Eq '(^|\.)0[0-9]+($|\.)'
		;;
	*) return 0 ;;
	esac
}

semver_compare() {
	LC_ALL=C awk -v a="$1" -v b="$2" '
		function normalize(value) {
			sub(/^0+/, "", value)
			return value == "" ? "0" : value
		}
		function compare_number(a, b, left, right) {
			left = normalize(a)
			right = normalize(b)
			if (length(left) != length(right)) return length(left) < length(right) ? -1 : 1
			if (left == right) return 0
			return ("x" left) < ("x" right) ? -1 : 1
		}
		function compare_identifier(a, b, a_number, b_number) {
			a_number = a ~ /^[0-9]+$/
			b_number = b ~ /^[0-9]+$/
			if (a_number && b_number) return compare_number(a, b)
			if (a_number != b_number) return a_number ? -1 : 1
			if (a == b) return 0
			return ("x" a) < ("x" b) ? -1 : 1
		}
		function compare(a, b, a_pos, b_pos, a_core_value, b_core_value, a_pre_value, b_pre_value, a_core, b_core, a_pre, b_pre, a_count, b_count, count, i, result) {
			a_pos = index(a, "-")
			b_pos = index(b, "-")
			a_core_value = a_pos ? substr(a, 1, a_pos - 1) : a
			b_core_value = b_pos ? substr(b, 1, b_pos - 1) : b
			a_pre_value = a_pos ? substr(a, a_pos + 1) : ""
			b_pre_value = b_pos ? substr(b, b_pos + 1) : ""
			split(a_core_value, a_core, ".")
			split(b_core_value, b_core, ".")
			for (i = 1; i <= 3; i++) {
				result = compare_number(a_core[i], b_core[i])
				if (result != 0) return result
			}
			if (!a_pos && !b_pos) return 0
			if (!a_pos) return 1
			if (!b_pos) return -1
			a_count = split(a_pre_value, a_pre, ".")
			b_count = split(b_pre_value, b_pre, ".")
			count = a_count < b_count ? a_count : b_count
			for (i = 1; i <= count; i++) {
				result = compare_identifier(a_pre[i], b_pre[i])
				if (result != 0) return result
			}
			if (a_count == b_count) return 0
			return a_count < b_count ? -1 : 1
		}
		BEGIN { print compare(a, b) }
	'
}

print_header() {
	printf '%s' "$CURSOR_HIDE"
	printf '\n'
	printf '  %s%sKiln%s %s%s%s\n' "$BOLD" "$CYAN" "$RESET" "$BOLD" "$T_TITLE" "$RESET"
	[ "$DRY_RUN" = 1 ] && printf '  %s%s%s\n' "$DIM" "$T_DRY" "$RESET"
	printf '\n'
}

uninstall_flow() {
	installed_path=""
	service_present=0
	if [ "$DRY_RUN" = 1 ] && [ -n "${KILN_SIM_INSTALLED:-}" ]; then
		installed_path="${DIR_REQ:-/usr/local/bin}/kiln"
	elif [ -n "$DIR_REQ" ]; then
		candidate="${DIR_REQ%/}/kiln"
		if [ -e "$candidate" ] || [ -L "$candidate" ]; then installed_path="$candidate"; fi
	elif [ -f /usr/local/bin/kiln ] && [ ! -L /usr/local/bin/kiln ]; then
		installed_path="/usr/local/bin/kiln"
	elif [ -n "${HOME:-}" ] && [ -f "$HOME/.local/bin/kiln" ] && [ ! -L "$HOME/.local/bin/kiln" ]; then
		installed_path="$HOME/.local/bin/kiln"
	fi
	[ -f "$SERVICE_UNIT" ] && service_present=1
	[ "$DRY_RUN" = 1 ] && [ "${KILN_SIM_SERVICE:-0}" = 1 ] && service_present=1
	if [ -z "$installed_path" ] && [ "$service_present" != 1 ]; then
		printf '  %s%s%s\n\n' "$DIM" "$T_RM_NONE" "$RESET"
		exit 0
	fi
	remove_label="$installed_path"
	[ -n "$remove_label" ] || remove_label="kiln.service"
	[ -z "$installed_path" ] || [ "$service_present" != 1 ] || remove_label="$installed_path + kiln.service"
	confirm_default="n"
	[ "$ASSUME_YES" = 1 ] && confirm_default="y"
	# shellcheck disable=SC2059
	reply="$(ask "$(printf "$T_CONFIRM_RM" "$remove_label")" "$confirm_default")"
	case "$reply" in
	y | Y | yes | YES) ;;
	*)
		printf '  %s%s%s\n\n' "$DIM" "$T_CANCELLED" "$RESET"
		exit 0
		;;
	esac
	if [ "$DRY_RUN" != 1 ]; then
		if [ "$service_present" = 1 ]; then
			uid="$(id -u 2>/dev/null || true)"
			[ "$uid" = 0 ] || die 1 "$T_RM_SVC_ROOT"
			command -v systemctl >/dev/null 2>&1 || die 1 "$T_RM_SVC_FAIL"
		fi
		if [ -n "$installed_path" ] && [ ! -w "$(dirname "$installed_path")" ]; then
			die 1 "$T_DIR_RO$(dirname "$installed_path")" "sudo rm -f $installed_path"
		fi
		if [ "$service_present" = 1 ]; then
			systemctl disable --now kiln >/dev/null 2>&1 || die 1 "$T_RM_SVC_FAIL"
			rm -f "$SERVICE_UNIT" || die 1 "$T_RM_SVC_FAIL"
			systemctl daemon-reload >/dev/null 2>&1 || die 1 "$T_RM_SVC_FAIL"
			warn "$T_RM_SVC"
		fi
		[ -z "$installed_path" ] || rm -f "$installed_path" || die 1 "$T_DIR_RO$(dirname "$installed_path")"
	fi
	mark=""
	[ "$DRY_RUN" = 1 ] && mark="${DIM}${T_DRYMARK}${RESET}"
	step_done "1/1" "$L_REMOVE" "${remove_label}${mark}"
	printf '\n'
	printf '  %s──────────────────────────────────────────────────%s\n' "$DIM" "$RESET"
	printf '   %s%s%s%s\n' "$GREEN" "$BOLD" "$T_DONE_RM" "$RESET"
	printf '\n'
	printf '     %s%s%s\n' "$DIM" "$T_RM_HINT" "$RESET"
	printf '  %s──────────────────────────────────────────────────%s\n\n' "$DIM" "$RESET"
	exit 0
}

if [ "$ACTION" = "remove" ]; then
	print_header
	uninstall_flow
fi

if [ -n "$VERSION_INPUT" ] && ! valid_version "$VERSION_REQ"; then
	die 1 "$T_BAD_VERSION$VERSION_INPUT"
fi

os="$(uname -s)"
case "$os" in
Linux) os="linux" ;;
Darwin) os="darwin" ;;
MINGW* | MSYS* | CYGWIN* | Windows_NT) die 2 "$T_PLAT_UNSUP$os" "$T_PLAT_WIN" ;;
*) die 2 "$T_PLAT_UNSUP$(uname -s)" "$T_NET_HINT_2" ;;
esac
arch="$(uname -m)"
case "$arch" in
x86_64 | amd64) arch="amd64" ;;
aarch64 | arm64) arch="arm64" ;;
armv7l | armv8l) arch="armv7" ;;
armv6l) arch="armv6" ;;
*) die 2 "$T_PLAT_UNSUP$arch" "$T_NET_HINT_2" ;;
esac
if [ "$os" = "darwin" ] && [ "$arch" = "amd64" ]; then
	if (sysctl hw.optional.arm64 2>/dev/null || true) | grep -q ': 1'; then
		arch="arm64"
	fi
fi
[ "$VARIANT" = "lite" ] && [ "$os" != "linux" ] && die 2 "$T_LITE_LINUX"

DLD=""
missing=""
SHACMD=""
if [ "$DRY_RUN" != 1 ]; then
	if command -v curl >/dev/null 2>&1; then
		DLD="curl"
	elif command -v wget >/dev/null 2>&1; then
		DLD="wget"
	else
		missing=" curl"
	fi
	for tool in tar mktemp; do
		command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
	done
	if command -v sha256sum >/dev/null 2>&1; then
		SHACMD="sha256sum"
	elif command -v shasum >/dev/null 2>&1; then
		SHACMD="shasum -a 256"
	else
		missing="$missing sha256sum"
	fi
	if [ -n "$missing" ]; then
		hint="apt install${missing}"
		command -v apk >/dev/null 2>&1 && hint="apk add${missing}"
		command -v dnf >/dev/null 2>&1 && hint="dnf install${missing}"
		command -v pacman >/dev/null 2>&1 && hint="pacman -S${missing}"
		command -v brew >/dev/null 2>&1 && hint="brew install${missing}"
		die 1 "$T_TOOL_MISS$missing" "$T_TOOL_HINT" "  $hint"
	fi
fi

if [ -n "$DIR_REQ" ]; then
	target="$DIR_REQ"
	[ ! -e "$target" ] || [ -d "$target" ] || die 1 "$T_DIR_TYPE$target"
	probe_dir="$target"
	while [ ! -d "$probe_dir" ]; do probe_dir="$(dirname "$probe_dir")"; done
	[ -w "$probe_dir" ] || die 1 "$T_DIR_RO$target"
else
	target="/usr/local/bin"
	if [ ! -d "$target" ] || [ ! -w "$target" ]; then
		[ -n "${HOME:-}" ] || die 1 "$T_HOME_MISS"
		target="$HOME/.local/bin"
	fi
fi

if [ "$WITH_SERVICE" = 1 ]; then
	if [ "$os" != "linux" ] || ! command -v systemctl >/dev/null 2>&1; then
		die 1 "$T_SVC_LINUX"
	fi
	case "$target" in
	/*) ;;
	*) die 1 "$T_SVC_DIR" ;;
	esac
	if [ -z "$SYSTEM_ROOT" ]; then
		case "$target" in
			*[[:space:]]* | /home | /home/* | /root | /root/* | /tmp | /tmp/* | /var/tmp | /var/tmp/*) die 1 "$T_SVC_DIR" ;;
		esac
	fi
	SERVICE_NOLOGIN=""
	if [ "$DRY_RUN" != 1 ]; then
		[ "$(id -u 2>/dev/null || true)" = 0 ] || die 1 "$T_SVC_ROOT"
		if ! id kiln >/dev/null 2>&1; then
			command -v useradd >/dev/null 2>&1 || die 1 "$T_TOOL_MISS useradd"
			for shell_path in /usr/sbin/nologin /sbin/nologin /bin/false; do
				if [ -x "$shell_path" ]; then SERVICE_NOLOGIN="$shell_path"; break; fi
			done
			[ -n "$SERVICE_NOLOGIN" ] || die 1 "$T_TOOL_MISS nologin"
		fi
	fi
fi

[ "$DRY_RUN" = 1 ] || WORK="$(mktemp -d)"
print_header

installed_version=""
installed_variant=""
installed_path="$target/kiln"
[ ! -d "$installed_path" ] || die 1 "$T_BIN_DIR$installed_path"
if [ "$DRY_RUN" = 1 ] && [ -n "${KILN_SIM_INSTALLED:-}" ]; then
	installed_version="${KILN_SIM_INSTALLED#v}"
	installed_variant="${KILN_SIM_VARIANT:-$VARIANT}"
	installed_path="$target/kiln"
elif [ -x "$installed_path" ]; then
	installed_info="$("$installed_path" -version 2>/dev/null || true)"
	installed_version="$(printf '%s\n' "$installed_info" | sed -n 's/.*version=\([^ ]*\).*/\1/p' | head -n 1)"
	installed_variant="$(printf '%s\n' "$installed_info" | sed -n 's/.*variant=\([^ ]*\).*/\1/p' | head -n 1)"
else
	installed_path=""
fi

resolve_latest() {
	base="$1"
	api_base="https://api.github.com"
	[ "$base" = "$GITHUB" ] || api_base="${base%/"$GITHUB"}/https://api.github.com"
	if [ "$DLD" = "curl" ]; then
		resolved="$(curl -fsSIL --proto '=https' --tlsv1.2 --connect-timeout "$PROBE_TIMEOUT" --max-time "$PROBE_TIMEOUT" \
			-o /dev/null -w '%{url_effective}' "$base/$REPO/releases/latest" 2>/dev/null |
			sed -n 's/.*\/tag\/v\{0,1\}\([^\/]*\)$/\1/p')"
		if valid_version "$resolved"; then
			printf '%s\n' "$resolved"
			return 0
		fi
		curl -fsSL --proto '=https' --tlsv1.2 --connect-timeout "$PROBE_TIMEOUT" --max-time "$PROBE_TIMEOUT" \
			"$api_base/repos/$REPO/releases/latest" 2>/dev/null |
			sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1
	else
		resolved="$(wget -q -T "$PROBE_TIMEOUT" -O - "$base/$REPO/releases/latest" 2>/dev/null |
			tr '"' '\n' | sed -n "s#^/$REPO/releases/tag/v\\{0,1\\}##p" | head -n 1)"
		if valid_version "$resolved"; then
			printf '%s\n' "$resolved"
			return 0
		fi
		wget -q -T "$PROBE_TIMEOUT" -O - "$api_base/repos/$REPO/releases/latest" 2>/dev/null |
			sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1
	fi
}

version=""
if [ -n "$VERSION_REQ" ]; then
	version="$VERSION_REQ"
elif [ "$DRY_RUN" = 1 ]; then
	version="1.1.0"
else
	(resolve_latest "$GITHUB" >"$WORK/ver" 2>/dev/null || true) &
	spin_while "$T_FETCH" "$!"
	version="$(cat "$WORK/ver" 2>/dev/null || true)"
	[ -n "$version" ] || die 3 "$T_NET_FAIL" "$T_NET_HINT_1" "$T_NET_HINT_2"
fi
if ! valid_version "$version"; then
	# shellcheck disable=SC2059
	die 3 "$(printf "$T_VER_FAIL" "v$version")" "$T_NET_HINT_2"
fi

name="kiln"
[ "$VARIANT" = "lite" ] && name="kiln-lite"
archive="${name}_${version}_${os}_${arch}.tar.gz"
asset_path="$REPO/releases/download/v${version}/${archive}"

source_base="$GITHUB"
source_label="$T_SRC_DIRECT"
if [ -n "$MIRROR_REQ" ]; then
	source_base="$MIRROR_REQ/$GITHUB"
	source_label="${MIRROR_REQ#https://}${P_L}${T_MIRROR_TAG}${P_R}"
elif [ "$DRY_RUN" = 1 ]; then
	if [ "${KILN_SIM_CN:-0}" = 1 ] && [ "$NO_MIRROR" != 1 ]; then
		source_base="https://ghfast.top/$GITHUB"
		source_label="ghfast.top${P_L}${T_MIRROR_TAG}${P_R}"
		warn "$T_SRC_MIRROR_NOTE"
		printf '\n'
	fi
else
	(probe_ok "$GITHUB/$asset_path" 2>/dev/null && touch "$WORK/ok_direct") >/dev/null 2>&1 &
	spin_while "$T_PROBE" "$!"
	picked=""
	[ ! -e "$WORK/ok_direct" ] || picked="$GITHUB"
	if [ -z "$picked" ] && [ "$NO_MIRROR" != 1 ]; then
		(
			i=0
			for candidate in $MIRRORS; do
				(probe_ok "$candidate/$GITHUB/$asset_path" 2>/dev/null && touch "$WORK/ok_$i") &
				i=$((i + 1))
			done
			wait
		) >/dev/null 2>&1 &
		spin_while "$T_PROBE" "$!"
		i=0
		for candidate in $MIRRORS; do
			if [ -e "$WORK/ok_$i" ] && [ -z "$picked" ]; then
				picked="$candidate"
			fi
			i=$((i + 1))
		done
	fi
	if [ -z "$picked" ]; then
		if [ -n "$VERSION_REQ" ]; then
			# shellcheck disable=SC2059
			die 3 "$(printf "$T_VER_FAIL" "v$version")" "$T_NET_HINT_2"
		fi
		die 3 "$T_NET_FAIL" "$T_NET_HINT_1" "$T_NET_HINT_2"
	fi
	if [ "$picked" = "$GITHUB" ]; then
		source_base="$GITHUB"
	else
		source_base="$picked/$GITHUB"
		source_label="${picked#https://}${P_L}${T_MIRROR_TAG}${P_R}"
		warn "$T_SRC_MIRROR_NOTE"
		printf '\n'
	fi
fi

action="install"
if [ -n "$installed_version" ]; then
	if valid_version "$installed_version" && valid_version "$version"; then
		comparison="$(semver_compare "$installed_version" "$version")"
		case "$comparison" in
		-1) action="upgrade" ;;
		0)
			if [ "$installed_variant" = "$VARIANT" ]; then action="same"; else action="switch"; fi
			;;
		1) action="downgrade" ;;
		esac
	fi
	if [ "$action" = "same" ] && [ "$ASSUME_YES" = 1 ] && [ "$WITH_SERVICE" != 1 ]; then
		# shellcheck disable=SC2059
		printf "  ${GREEN}✓${RESET} $T_SAME\n\n" "$installed_version"
		exit 0
	fi
	if [ "$ASSUME_YES" != 1 ] && { [ -t 0 ] || [ -r /dev/tty ]; }; then
		# shellcheck disable=SC2059
		printf "  ${BOLD}$T_ACT_ASK${RESET}\n" "$installed_version"
		if [ "$action" = "same" ]; then
			# shellcheck disable=SC2059
			printf "     $T_ACT_RE\n" "$version"
		elif [ "$action" = "switch" ]; then
			# shellcheck disable=SC2059
			printf "     $T_ACT_SWITCH\n" "$VARIANT"
		else
			# shellcheck disable=SC2059
			printf "     $T_ACT_UP\n" "$version"
		fi
		printf '     %s\n' "$T_ACT_RM"
		printf '     %s\n' "$T_ACT_CANCEL"
		default_choice="1"
		[ "$action" = "same" ] && default_choice="3"
		prompt="$T_ACT_P_UP"
		[ "$action" = "same" ] && prompt="$T_ACT_P_SAME"
		choice="$(ask "$prompt" "$default_choice")"
		printf '\n'
		case "$choice" in
		1) [ "$action" = "same" ] && action="install" ;;
		2) uninstall_flow ;;
		*)
			printf '  %s%s%s\n\n' "$DIM" "$T_CANCELLED" "$RESET"
			exit 0
			;;
		esac
	fi
fi

decoder_label="$T_DEC_NO"
command -v ffmpeg >/dev/null 2>&1 && decoder_label="$T_DEC_YES"
version_note="$T_LATEST"
[ -n "$VERSION_REQ" ] && version_note=""
# shellcheck disable=SC2059
[ "$action" = "upgrade" ] && version_note="$(printf "$T_UP_FROM" "$installed_version")"
# shellcheck disable=SC2059
[ "$action" = "downgrade" ] && version_note="$(printf "$T_DOWN_TO" "$installed_version")"
# shellcheck disable=SC2059
[ "$action" = "switch" ] && version_note="$(printf "$T_SWITCH_FROM" "${installed_variant:-unknown}")"

printf '  %s%s%s\n' "$BOLD" "$T_PLAN" "$RESET"
printf '     %s%s%s  v%s%s%s%s\n' "$DIM" "$T_VER" "$RESET" "$version" "$DIM" "$version_note" "$RESET"
printf '     %s%s%s  %s\n' "$DIM" "$T_VARIANT" "$RESET" "$VARIANT"
printf '     %s%s%s  %s/%s\n' "$DIM" "$T_PLAT" "$RESET" "$os" "$arch"
printf '     %s%s%s  %s/kiln\n' "$DIM" "$T_DEST" "$RESET" "$target"
printf '     %s%s%s  %s\n' "$DIM" "$T_SRC" "$RESET" "$source_label"
printf '     %s%s%s  %s%s%s\n' "$DIM" "$T_DEC" "$RESET" "$DIM" "$decoder_label" "$RESET"
[ "$WITH_SERVICE" = 1 ] && printf '     %s%s%s  %s\n' "$DIM" "$T_SVC" "$RESET" "$T_SVC_ON"
printf '\n'

if [ "$ASSUME_YES" != 1 ]; then
	confirm="$T_CONFIRM"
	confirm_default="y"
	[ "$action" = "upgrade" ] && confirm="$T_CONFIRM_UP"
	if [ "$action" = "downgrade" ]; then
		confirm="$T_CONFIRM_DOWN"
		confirm_default="n"
	fi
	reply="$(ask "$confirm" "$confirm_default")"
	case "$reply" in
	y | Y | yes | YES) ;;
	*)
		printf '  %s%s%s\n\n' "$DIM" "$T_CANCELLED" "$RESET"
		exit 0
		;;
	esac
	printf '\n'
fi

total=3
service_started=0
[ "$WITH_SERVICE" = 1 ] && total=4

if [ "$DRY_RUN" = 1 ]; then
	:
elif [ "$DLD" = "curl" ]; then
	printf '  %s[1/%s]%s %s\n' "$DIM" "$total" "$RESET" "$L_DL"
	curl -fL --proto '=https' --tlsv1.2 --retry 2 --connect-timeout 10 --speed-limit 1024 --speed-time 30 \
		--max-time 300 --progress-bar -o "$WORK/$archive" "$source_base/$asset_path" ||
		die 3 "$T_NET_FAIL" "$T_NET_HINT_1" "$T_NET_HINT_2"
	[ "$ANIMATE" = 1 ] && printf '\033[2A\033[K'
else
	(wget -q -T 300 -O "$WORK/$archive" "$source_base/$asset_path" 2>/dev/null || : >"$WORK/dl_fail") &
	spin_while "$L_DL" "$!"
	[ -e "$WORK/dl_fail" ] && die 3 "$T_NET_FAIL" "$T_NET_HINT_1" "$T_NET_HINT_2"
fi
mark=""
[ "$DRY_RUN" = 1 ] && mark="${DIM}${T_DRYMARK}${RESET}"
step_done "1/$total" "$L_DL" "${archive}${mark}"

if [ "$DRY_RUN" = 1 ]; then
	if [ "${KILN_SIM_FAIL:-0}" = 1 ]; then
		printf '%s  %s[2/%s]%s %s%s✗%s %s\n' "$CLEAR_LINE" "$DIM" "$total" "$RESET" "$L_VERIFY" "$RED" "$RESET" "$T_MISMATCH"
		printf '\n'
		printf '  %s%s%s\n' "$RED" "$T_FAIL_SUM_1" "$RESET"
		printf '  %s%s%s\n\n' "$DIM" "$T_FAIL_SUM_2" "$RESET"
		exit 4
	fi
else
	sums_url="$GITHUB/$REPO/releases/download/v${version}/SHA256SUMS"
	fetch "$sums_url" "$WORK/SHA256SUMS" 10 2>/dev/null ||
		die 3 "$T_NET_FAIL" "$T_NET_HINT_1" "$T_NET_HINT_2"
	expected="$(awk -v f="$archive" '$2 == f { print $1 }' "$WORK/SHA256SUMS")"
	actual="$($SHACMD "$WORK/$archive" | awk '{ print $1 }')"
	if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
		printf '%s  %s[2/%s]%s %s%s✗%s %s\n' "$CLEAR_LINE" "$DIM" "$total" "$RESET" "$L_VERIFY" "$RED" "$RESET" "$T_MISMATCH"
		printf '\n'
		printf '  %s%s%s\n' "$RED" "$T_FAIL_SUM_1" "$RESET"
		printf '  %s%s%s\n\n' "$DIM" "$T_FAIL_SUM_2" "$RESET"
		exit 4
	fi
fi
step_done "2/$total" "$L_VERIFY" "${T_MATCH}${DIM}${T_MATCH_SRC}${RESET}"

if [ "$DRY_RUN" != 1 ]; then
	mkdir -p "$WORK/unpack" || die 1 "$T_INSTALL_FAIL"
	tar -xzf "$WORK/$archive" -C "$WORK/unpack" || die 1 "$T_INSTALL_FAIL"
	[ -f "$WORK/unpack/$name" ] || die 1 "$archive: $name missing"
	mkdir -p "$target" || die 1 "$T_INSTALL_FAIL"
	STAGED="$(mktemp "$target/.kiln.new.XXXXXX")" || die 1 "$T_INSTALL_FAIL"
	cp "$WORK/unpack/$name" "$STAGED" || die 1 "$T_INSTALL_FAIL"
	chmod 0755 "$STAGED" || die 1 "$T_INSTALL_FAIL"
	if ! staged_info="$("$STAGED" -version 2>/dev/null)"; then
		rm -f "$STAGED"
		STAGED=""
		die 1 "$T_RUN_FAIL" "$T_RUN_FAIL_HINT"
	fi
	staged_version="$(printf '%s\n' "$staged_info" | sed -n 's/.*version=\([^ ]*\).*/\1/p' | head -n 1)"
	staged_variant="$(printf '%s\n' "$staged_info" | sed -n 's/.*variant=\([^ ]*\).*/\1/p' | head -n 1)"
	if [ "$staged_version" != "$version" ] || [ "$staged_variant" != "$VARIANT" ]; then
		rm -f "$STAGED"
		STAGED=""
		die 1 "$T_ID_FAIL"
	fi
	if [ "$WITH_SERVICE" = 1 ]; then
		if [ -f "$target/kiln" ] || [ -L "$target/kiln" ]; then
			BINARY_BACKUP="$(mktemp "$target/.kiln.old.XXXXXX")" || die 1 "$T_INSTALL_FAIL"
			cp -p "$target/kiln" "$BINARY_BACKUP" || die 1 "$T_INSTALL_FAIL"
		fi
		if [ -f "$SERVICE_UNIT" ] || [ -L "$SERVICE_UNIT" ]; then
			UNIT_BACKUP="$(mktemp "$(dirname "$SERVICE_UNIT")/.kiln.service.old.XXXXXX")" || die 1 "$T_SVC_FAIL"
			cp -p "$SERVICE_UNIT" "$UNIT_BACKUP" || die 1 "$T_SVC_FAIL"
		fi
		systemctl is-active --quiet kiln >/dev/null 2>&1 && SERVICE_WAS_ACTIVE=1
		systemctl is-enabled --quiet kiln >/dev/null 2>&1 && SERVICE_WAS_ENABLED=1
		SERVICE_ROLLBACK=1
	fi
	mv -f "$STAGED" "$target/kiln" || die 1 "$T_INSTALL_FAIL"
	STAGED=""
fi
step_done "3/$total" "$L_INSTALL" "${target}/kiln${mark}"

if [ "$WITH_SERVICE" = 1 ]; then
	svc_note=""
	if [ "$DRY_RUN" != 1 ]; then
		if ! id kiln >/dev/null 2>&1; then
			useradd -r -U -s "$SERVICE_NOLOGIN" -d /var/lib/kiln kiln || die 1 "$T_SVC_FAIL"
		fi
		service_group="$(id -gn kiln 2>/dev/null || true)"
		[ -n "$service_group" ] || die 1 "$T_SVC_FAIL"
		mkdir -p "$SERVICE_CONFIG_DIR" "$SERVICE_DATA" || die 1 "$T_SVC_FAIL"
		chown "kiln:$service_group" "$SERVICE_DATA" || die 1 "$T_SVC_FAIL"
		STAGED="$(mktemp "$(dirname "$SERVICE_UNIT")/.kiln.service.new.XXXXXX")" || die 1 "$T_SVC_FAIL"
		if ! cat >"$STAGED" <<EOF
[Unit]
Description=Kiln
After=network-online.target
Wants=network-online.target

[Service]
User=kiln
Group=${service_group}
ExecStart=${target}/kiln -config /etc/kiln/kiln.toml
WorkingDirectory=/var/lib/kiln
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/kiln

[Install]
WantedBy=multi-user.target
EOF
		then
			die 1 "$T_SVC_FAIL"
		fi
		chmod 0644 "$STAGED" || die 1 "$T_SVC_FAIL"
		mv -f "$STAGED" "$SERVICE_UNIT" || die 1 "$T_SVC_FAIL"
		STAGED=""
		systemctl daemon-reload >/dev/null 2>&1 || die 1 "$T_SVC_FAIL"
		if [ -f "$SERVICE_CONFIG" ]; then
			systemctl enable kiln >/dev/null 2>&1 || die 1 "$T_SVC_FAIL"
			systemctl restart kiln >/dev/null 2>&1 || die 1 "$T_SVC_FAIL"
			service_started=1
		else
			systemctl disable --now kiln >/dev/null 2>&1 || die 1 "$T_SVC_FAIL"
			svc_note="$T_SVC_CFG_MISS"
		fi
		SERVICE_ROLLBACK=0
	fi
	step_done "4/$total" "$L_SERVICE" "kiln.service${mark}"
	[ -n "$svc_note" ] && warn "$svc_note"
fi

done_title="$T_DONE"
[ "$action" = "upgrade" ] && done_title="$T_DONE_UP"

printf '\n'
printf '  %s──────────────────────────────────────────────────%s\n' "$DIM" "$RESET"
printf '   %s%s%s%s   kiln v%s%s%s%s/%s%s%s\n' "$GREEN" "$BOLD" "$done_title" "$RESET" "$version" "$DIM" "$P_L" "$os" "$arch" "$P_R" "$RESET"
printf '\n'
printf '     %s%s%s    %s/kiln\n' "$DIM" "$C_BIN" "$RESET" "$target"
printf '     %s%s%s    %s\n' "$DIM" "$C_RUN" "$RESET" "$C_RUN_V"
printf '     %s%s%s    %s\n' "$DIM" "$C_UP" "$RESET" "$C_UP_V"
printf '     %s%s%s    %s %s\n' "$DIM" "$C_RM" "$RESET" "$C_RM_V" "$target"
[ "$WITH_SERVICE" != 1 ] || [ "$service_started" != 1 ] || printf '     %s%s%s    systemctl status kiln\n' "$DIM" "$C_STATUS" "$RESET"
[ "$WITH_SERVICE" != 1 ] || [ "$service_started" != 1 ] || printf '     %s%s%s    journalctl -u kiln -f\n' "$DIM" "$C_LOG" "$RESET"
[ "$WITH_SERVICE" != 1 ] || [ "$service_started" = 1 ] || printf '     %s%s%s    systemctl enable --now kiln\n' "$DIM" "$C_ENABLE" "$RESET"
printf '     %s%s%s    https://github.com/%s\n' "$DIM" "$C_DOC" "$RESET" "$REPO"
printf '  %s──────────────────────────────────────────────────%s\n' "$DIM" "$RESET"

case ":$PATH:" in
*":$target:"*) ;;
*)
	# shellcheck disable=SC2059
	warn "$(printf "$T_PATH_WARN" "$target")"
	shell_name="$(basename "${SHELL:-/bin/sh}")"
	# shellcheck disable=SC2016
	case "$shell_name" in
	zsh) printf '     %secho '\''export PATH="%s:$PATH"'\'' >> ~/.zshrc && source ~/.zshrc%s\n' "$CYAN" "$target" "$RESET" ;;
	fish) printf '     %sfish_add_path %s%s\n' "$CYAN" "$target" "$RESET" ;;
	*) printf '     %secho '\''export PATH="%s:$PATH"'\'' >> ~/.bashrc && source ~/.bashrc%s\n' "$CYAN" "$target" "$RESET" ;;
	esac
	;;
esac
printf '\n'
