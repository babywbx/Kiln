#!/bin/sh

set -eu

SELF="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)/$(basename -- "$0")"
BASE_PATH="/usr/bin:/bin:/usr/sbin:/sbin"

fake_curl() {
	out=""
	url=""
	head=0
	while [ $# -gt 0 ]; do
		case "$1" in
		-o)
			shift
			out="$1"
			;;
		-w) shift ;;
		-*) case "$1" in *I*) head=1 ;; esac ;;
		*) url="$1" ;;
		esac
		shift
	done
	case "$url" in
	*/releases/latest)
		printf 'https://github.com/babywbx/Kiln/releases/tag/v%s' "$KILN_TEST_RELEASE_VERSION"
		;;
	*/SHA256SUMS)
		cp "$KILN_TEST_SUMS" "$out"
		;;
	*.tar.gz)
		if [ "$head" = 1 ]; then
			sleep "${KILN_TEST_PROBE_DELAY:-0}"
		else
			cp "$KILN_TEST_ARCHIVE" "$out"
		fi
		;;
	*) exit 22 ;;
	esac
}

fake_uname() {
	case "${1:-}" in
	-s) printf 'Linux\n' ;;
	-m) printf 'x86_64\n' ;;
	*) printf 'Linux\n' ;;
	esac
}

fake_wget() {
	out="-"
	url=""
	spider=0
	while [ $# -gt 0 ]; do
		case "$1" in
		-O)
			shift
			out="$1"
			;;
		--spider) spider=1 ;;
		-T | -t) shift ;;
		-*) ;;
		*) url="$1" ;;
		esac
		shift
	done
	[ -z "${KILN_TEST_LOG:-}" ] || printf '%s\n' "$url" >>"$KILN_TEST_LOG"
	case "$url" in
	*/releases/latest)
		printf '{"tag_name":"v%s"}\n' "$KILN_TEST_RELEASE_VERSION"
		;;
	*/SHA256SUMS)
		cp "$KILN_TEST_SUMS" "$out"
		;;
	*.tar.gz)
		if [ "$spider" = 1 ]; then
			sleep "${KILN_TEST_PROBE_DELAY:-0}"
		else
			cp "$KILN_TEST_ARCHIVE" "$out"
		fi
		;;
	*) exit 8 ;;
	esac
}

fake_systemctl() {
	[ -z "${KILN_TEST_SYSTEMCTL_LOG:-}" ] || printf '%s\n' "$*" >>"$KILN_TEST_SYSTEMCTL_LOG"
	if [ -n "${KILN_TEST_SYSTEMCTL_FAIL:-}" ] && [ "$*" = "$KILN_TEST_SYSTEMCTL_FAIL" ]; then
		if [ -z "${KILN_TEST_SYSTEMCTL_FAIL_MARKER:-}" ] || [ ! -e "$KILN_TEST_SYSTEMCTL_FAIL_MARKER" ]; then
			[ -z "${KILN_TEST_SYSTEMCTL_FAIL_MARKER:-}" ] || : >"$KILN_TEST_SYSTEMCTL_FAIL_MARKER"
			exit 1
		fi
	fi
	case "${1:-}" in
	is-enabled)
		[ -z "${KILN_TEST_SYSTEMCTL_ENABLED_STATE:-}" ] || [ -e "$KILN_TEST_SYSTEMCTL_ENABLED_STATE" ]
		exit
		;;
	enable)
		[ -z "${KILN_TEST_SYSTEMCTL_ENABLED_STATE:-}" ] || : >"$KILN_TEST_SYSTEMCTL_ENABLED_STATE"
		;;
	disable)
		[ -z "${KILN_TEST_SYSTEMCTL_ENABLED_STATE:-}" ] || rm -f "$KILN_TEST_SYSTEMCTL_ENABLED_STATE"
		;;
	esac
	exit 0
}

fake_id() {
	case "${1:-}" in
	-u) printf '%s\n' "${KILN_TEST_UID:-0}" ;;
	-gn) printf 'kiln\n' ;;
	kiln) [ "${KILN_TEST_USER_EXISTS:-1}" = 1 ] ;;
	*) exit 1 ;;
	esac
}

fake_binary() {
	version="${KILN_TEST_BINARY_VERSION:-${KILN_TEST_RELEASE_VERSION:-1.0.0}}"
	variant="${KILN_TEST_BINARY_VARIANT:-${KILN_TEST_RELEASE_VARIANT:-full}}"
	if [ -f "$0.meta" ]; then
		read -r version variant <"$0.meta"
	fi
	[ "${KILN_TEST_BINARY_FAIL:-0}" != 1 ] || exit 126
	[ "${1:-}" = -version ] || exit 2
	printf 'kiln version=%s commit=test built_at=test variant=%s\n' "$version" "$variant"
}

case "$(basename -- "$0")" in
curl) fake_curl "$@"; exit ;;
uname) fake_uname "$@"; exit ;;
wget) fake_wget "$@"; exit ;;
systemctl) fake_systemctl "$@" ;;
id) fake_id "$@"; exit ;;
chown) exit ;;
kiln | kiln-lite | .kiln.new.*) fake_binary "$@"; exit ;;
esac

HERE="$(dirname -- "$SELF")"
INSTALLER="$HERE/../install.sh"
RELEASE_WORKFLOW="$HERE/../.github/workflows/release.yml"
CI_WORKFLOW="$HERE/../.github/workflows/ci.yml"
TEST_SHELL="${TEST_SHELL:-/bin/sh}"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/kiln-install-test.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM

fail() {
	printf 'not ok - %s\n' "$1" >&2
	exit 1
}

make_case() {
	case_dir="$TEST_ROOT/$1"
	mkdir -p "$case_dir/fakebin" "$case_dir/home" "$case_dir/stage" "$case_dir/target" "$case_dir/tmp"
	cp "$SELF" "$case_dir/stage/kiln"
	chmod 0755 "$case_dir/stage/kiln"
	archive="$case_dir/kiln_1.2.3_linux_amd64.tar.gz"
	tar -czf "$archive" -C "$case_dir/stage" kiln
	if command -v sha256sum >/dev/null 2>&1; then
		hash="$(sha256sum "$archive" | awk '{print $1}')"
	else
		hash="$(shasum -a 256 "$archive" | awk '{print $1}')"
	fi
	printf '%s  %s\n' "$hash" "$(basename -- "$archive")" >"$case_dir/SHA256SUMS"
	ln -s "$SELF" "$case_dir/fakebin/curl"
	ln -s "$SELF" "$case_dir/fakebin/uname"
	printf '%s\n' "$case_dir"
}

link_base_tools() {
	fakebin="$1"
	for tool in awk basename cat chmod cp dirname grep gunzip gzip head mkdir mktemp mv rm sed sha256sum shasum sleep tar touch tr; do
		path="$(PATH="$BASE_PATH" command -v "$tool" 2>/dev/null || true)"
		[ -z "$path" ] || [ -e "$fakebin/$tool" ] || ln -s "$path" "$fakebin/$tool"
	done
}

test_probe_waits_for_sources() {
	case_dir="$(make_case probe-wait)"
	status=0
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin:$BASE_PATH" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_TEST_ARCHIVE="$case_dir/kiln_1.2.3_linux_amd64.tar.gz" \
		KILN_TEST_SUMS="$case_dir/SHA256SUMS" \
		KILN_TEST_RELEASE_VERSION=1.2.3 \
		KILN_TEST_RELEASE_VARIANT=full \
		KILN_TEST_PROBE_DELAY=1 \
		"$TEST_SHELL" "$INSTALLER" --version 1.2.3 --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || status=$?
	[ "$status" = 0 ] || fail "installer returned $status before delayed probes completed"
	[ -x "$case_dir/target/kiln" ] || fail "installer did not create the target binary"
	printf 'ok - waits for concurrent source probes\n'
}

test_failed_binary_preserves_existing_target() {
	case_dir="$(make_case staged-check)"
	printf 'old binary\n' >"$case_dir/target/kiln"
	status=0
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin:$BASE_PATH" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_TEST_ARCHIVE="$case_dir/kiln_1.2.3_linux_amd64.tar.gz" \
		KILN_TEST_SUMS="$case_dir/SHA256SUMS" \
		KILN_TEST_RELEASE_VERSION=1.2.3 \
		KILN_TEST_RELEASE_VARIANT=full \
		KILN_TEST_BINARY_FAIL=1 \
		"$TEST_SHELL" "$INSTALLER" --version 1.2.3 --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || status=$?
	[ "$status" != 0 ] || fail "installer accepted a binary that failed its version check"
	[ "$(sed -n '1p' "$case_dir/target/kiln")" = "old binary" ] || fail "failed binary replaced the existing target"
	for staged in "$case_dir/target"/.kiln.new.*; do
		[ ! -e "$staged" ] || fail "failed install left a staged binary"
	done
	printf 'ok - preserves existing target when staged binary fails\n'
}

test_binary_directory_is_rejected() {
	case_dir="$(make_case binary-directory)"
	mkdir "$case_dir/target/kiln"
	status=0
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin:$BASE_PATH" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_TEST_ARCHIVE="$case_dir/kiln_1.2.3_linux_amd64.tar.gz" \
		KILN_TEST_SUMS="$case_dir/SHA256SUMS" \
		KILN_TEST_RELEASE_VERSION=1.2.3 \
		KILN_TEST_RELEASE_VARIANT=full \
		"$TEST_SHELL" "$INSTALLER" --version 1.2.3 --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || status=$?
	[ "$status" = 1 ] || fail "installer accepted a directory as the target binary"
	[ -d "$case_dir/target/kiln" ] || fail "installer replaced the target directory"
	for staged in "$case_dir/target/kiln"/.kiln.new.*; do
		[ ! -e "$staged" ] || fail "failed install moved a staged binary into the target directory"
	done
	printf 'ok - rejects a directory at the target binary path\n'
}

test_wrong_binary_version_preserves_existing_target() {
	case_dir="$(make_case wrong-binary-version)"
	printf 'old binary\n' >"$case_dir/target/kiln"
	chmod 0755 "$case_dir/target/kiln"
	status=0
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin:$BASE_PATH" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_TEST_ARCHIVE="$case_dir/kiln_1.2.3_linux_amd64.tar.gz" \
		KILN_TEST_SUMS="$case_dir/SHA256SUMS" \
		KILN_TEST_RELEASE_VERSION=1.2.3 \
		KILN_TEST_RELEASE_VARIANT=full \
		KILN_TEST_BINARY_VERSION=9.9.9 \
		"$TEST_SHELL" "$INSTALLER" --version 1.2.3 --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || status=$?
	[ "$status" = 1 ] || fail "installer accepted a binary with the wrong version"
	[ "$(sed -n '1p' "$case_dir/target/kiln")" = "old binary" ] || fail "wrong-version binary replaced the existing target"
	printf 'ok - rejects a binary with the wrong version\n'
}

test_wrong_binary_variant_preserves_existing_target() {
	case_dir="$(make_case wrong-binary-variant)"
	printf 'old binary\n' >"$case_dir/target/kiln"
	chmod 0755 "$case_dir/target/kiln"
	status=0
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin:$BASE_PATH" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_TEST_ARCHIVE="$case_dir/kiln_1.2.3_linux_amd64.tar.gz" \
		KILN_TEST_SUMS="$case_dir/SHA256SUMS" \
		KILN_TEST_RELEASE_VERSION=1.2.3 \
		KILN_TEST_RELEASE_VARIANT=full \
		KILN_TEST_BINARY_VARIANT=lite \
		"$TEST_SHELL" "$INSTALLER" --version 1.2.3 --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || status=$?
	[ "$status" = 1 ] || fail "installer accepted a binary with the wrong variant"
	[ "$(sed -n '1p' "$case_dir/target/kiln")" = "old binary" ] || fail "wrong-variant binary replaced the existing target"
	printf 'ok - rejects a binary with the wrong variant\n'
}

test_stable_release_upgrades_prerelease() {
	case_dir="$(make_case prerelease-upgrade)"
	cp "$SELF" "$case_dir/target/kiln"
	chmod 0755 "$case_dir/target/kiln"
	printf '1.0.0-rc.1 full\n' >"$case_dir/target/kiln.meta"
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin:$BASE_PATH" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_DRY_RUN=1 \
		"$TEST_SHELL" "$INSTALLER" --version 1.0.0 --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || fail "stable release upgrade failed"
	grep -q 'upgrading from v1.0.0-rc.1' "$case_dir/output" || fail "stable release was not treated as newer than its prerelease"
	printf 'ok - stable release upgrades prerelease\n'
}

test_same_version_switches_variant() {
	case_dir="$(make_case variant-switch)"
	cp "$SELF" "$case_dir/target/kiln"
	chmod 0755 "$case_dir/target/kiln"
	printf '1.2.3 full\n' >"$case_dir/target/kiln.meta"
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin:$BASE_PATH" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_DRY_RUN=1 \
		"$TEST_SHELL" "$INSTALLER" --version 1.2.3 --lite --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || fail "same-version variant switch failed"
	grep -q 'kiln-lite_1.2.3_linux_amd64.tar.gz' "$case_dir/output" || fail "same version with a different variant was skipped"
	printf 'ok - same version switches variant\n'
}

test_same_version_still_configures_service() {
	case_dir="$(make_case same-version-service)"
	cp "$SELF" "$case_dir/target/kiln"
	chmod 0755 "$case_dir/target/kiln"
	printf '1.2.3 full\n' >"$case_dir/target/kiln.meta"
	ln -s "$SELF" "$case_dir/fakebin/systemctl"
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin:$BASE_PATH" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_DRY_RUN=1 \
		KILN_TEST_ROOT="$case_dir/root" \
		"$TEST_SHELL" "$INSTALLER" --version 1.2.3 --service --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || fail "same-version service setup failed"
	grep -q 'kiln.service' "$case_dir/output" || fail "same-version install skipped service setup"
	printf 'ok - configures service for an existing current binary\n'
}

test_service_upgrade_restarts_process() {
	case_dir="$(make_case service-restart)"
	cp "$SELF" "$case_dir/target/kiln"
	chmod 0755 "$case_dir/target/kiln"
	printf '1.0.0 full\n' >"$case_dir/target/kiln.meta"
	mkdir -p "$case_dir/root/etc/systemd/system" "$case_dir/root/etc/kiln"
	: >"$case_dir/root/etc/kiln/kiln.toml"
	: >"$case_dir/systemctl.log"
	for tool in systemctl id chown; do ln -s "$SELF" "$case_dir/fakebin/$tool"; done
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin:$BASE_PATH" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_TEST_ROOT="$case_dir/root" \
		KILN_TEST_SYSTEMCTL_LOG="$case_dir/systemctl.log" \
		KILN_TEST_ARCHIVE="$case_dir/kiln_1.2.3_linux_amd64.tar.gz" \
		KILN_TEST_SUMS="$case_dir/SHA256SUMS" \
		KILN_TEST_RELEASE_VERSION=1.2.3 \
		KILN_TEST_RELEASE_VARIANT=full \
		"$TEST_SHELL" "$INSTALLER" --version 1.2.3 --service --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || fail "service upgrade failed"
	grep -q '^restart kiln$' "$case_dir/systemctl.log" || fail "service upgrade did not restart the running process"
	printf 'ok - restarts the service after an upgrade\n'
}

test_service_failure_restores_binary_and_unit() {
	case_dir="$(make_case service-rollback)"
	cp "$SELF" "$case_dir/target/kiln"
	printf '\n# old binary marker\n' >>"$case_dir/target/kiln"
	chmod 0755 "$case_dir/target/kiln"
	cp "$case_dir/target/kiln" "$case_dir/old-kiln"
	printf '1.0.0 full\n' >"$case_dir/target/kiln.meta"
	mkdir -p "$case_dir/root/etc/systemd/system" "$case_dir/root/etc/kiln"
	printf 'old unit\n' >"$case_dir/root/etc/systemd/system/kiln.service"
	cp "$case_dir/root/etc/systemd/system/kiln.service" "$case_dir/old-unit"
	: >"$case_dir/root/etc/kiln/kiln.toml"
	: >"$case_dir/systemctl.log"
	for tool in systemctl id chown; do ln -s "$SELF" "$case_dir/fakebin/$tool"; done
	status=0
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin:$BASE_PATH" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_TEST_ROOT="$case_dir/root" \
		KILN_TEST_SYSTEMCTL_LOG="$case_dir/systemctl.log" \
		KILN_TEST_SYSTEMCTL_ENABLED_STATE="$case_dir/systemctl.enabled" \
		KILN_TEST_SYSTEMCTL_FAIL='restart kiln' \
		KILN_TEST_SYSTEMCTL_FAIL_MARKER="$case_dir/restart.failed" \
		KILN_TEST_ARCHIVE="$case_dir/kiln_1.2.3_linux_amd64.tar.gz" \
		KILN_TEST_SUMS="$case_dir/SHA256SUMS" \
		KILN_TEST_RELEASE_VERSION=1.2.3 \
		KILN_TEST_RELEASE_VARIANT=full \
		"$TEST_SHELL" "$INSTALLER" --version 1.2.3 --service --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || status=$?
	[ "$status" = 1 ] || fail "service restart failure returned $status"
	cmp -s "$case_dir/old-kiln" "$case_dir/target/kiln" || fail "service failure left the new binary installed"
	cmp -s "$case_dir/old-unit" "$case_dir/root/etc/systemd/system/kiln.service" || fail "service failure left the new unit installed"
	[ ! -e "$case_dir/systemctl.enabled" ] || fail "service failure left a previously disabled service enabled"
	printf 'ok - restores binary, unit, and service state on failure\n'
}

test_invalid_values_are_rejected() {
	case_dir="$(make_case invalid-values)"
	status=0
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 \
		"$TEST_SHELL" "$INSTALLER" --dry-run --version --lite >"$case_dir/missing-output" 2>&1 || status=$?
	[ "$status" = 1 ] || fail "option token was accepted as a version value"
	grep -q 'option needs a value: --version' "$case_dir/missing-output" || fail "missing version value was not explained"
	status=0
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 \
		"$TEST_SHELL" "$INSTALLER" --dry-run --version '1.2.3/../../escape' >"$case_dir/version-output" 2>&1 || status=$?
	[ "$status" = 1 ] || fail "unsafe version string was accepted"
	grep -q 'invalid version:' "$case_dir/version-output" || fail "invalid version was not explained"
	status=0
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 KILN_YES=1 \
		"$TEST_SHELL" "$INSTALLER" --dry-run --version v >"$case_dir/bare-v-output" 2>&1 || status=$?
	[ "$status" = 1 ] || fail "bare v was treated as the latest version"
	grep -q 'invalid version:' "$case_dir/bare-v-output" || fail "bare v was not explained"
	printf 'ok - rejects missing and unsafe option values\n'
}

test_mirror_options_are_consistent() {
	case_dir="$(make_case mirror-options)"
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 KILN_YES=1 \
		"$TEST_SHELL" "$INSTALLER" --dry-run --version 1.2.3 --mirror https://mirror.example/ --dir "$case_dir/target" \
		>"$case_dir/mirror-output" 2>&1 || fail "manual mirror dry-run failed"
	grep -q 'mirror.example (mirror)' "$case_dir/mirror-output" || fail "dry-run ignored the manual mirror"
	status=0
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 \
		"$TEST_SHELL" "$INSTALLER" --dry-run --version 1.2.3 --mirror https://mirror.example --no-mirror \
		>"$case_dir/conflict-output" 2>&1 || status=$?
	[ "$status" = 1 ] || fail "conflicting mirror options were accepted"
	status=0
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 \
		"$TEST_SHELL" "$INSTALLER" --dry-run --version 1.2.3 --mirror http://mirror.example \
		>"$case_dir/http-output" 2>&1 || status=$?
	[ "$status" = 1 ] || fail "insecure mirror URL was accepted"
	printf 'ok - keeps mirror options secure and consistent\n'
}

test_wget_resolves_latest_through_mirror() {
	case_dir="$(make_case wget-mirror)"
	rm -f "$case_dir/fakebin/curl"
	ln -s "$SELF" "$case_dir/fakebin/wget"
	link_base_tools "$case_dir/fakebin"
	: >"$case_dir/requests"
	status=0
	env \
		HOME="$case_dir/home" \
		PATH="$case_dir/fakebin" \
		TMPDIR="$case_dir/tmp" \
		NO_COLOR=1 \
		KILN_YES=1 \
		KILN_TEST_ARCHIVE="$case_dir/kiln_1.2.3_linux_amd64.tar.gz" \
		KILN_TEST_SUMS="$case_dir/SHA256SUMS" \
		KILN_TEST_RELEASE_VERSION=1.2.3 \
		KILN_TEST_RELEASE_VARIANT=full \
		KILN_TEST_LOG="$case_dir/requests" \
		"$TEST_SHELL" "$INSTALLER" --mirror https://mirror.example --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || status=$?
	[ "$status" = 0 ] || fail "wget mirror install returned $status"
	grep -q '^https://mirror.example/https://api.github.com/repos/babywbx/Kiln/releases/latest$' "$case_dir/requests" || fail "wget resolved latest outside the manual mirror"
	[ -x "$case_dir/target/kiln" ] || fail "wget mirror install did not create the target binary"
	printf 'ok - wget resolves latest through manual mirror\n'
}

test_uninstall_uses_explicit_dir_without_install_dependencies() {
	case_dir="$(make_case uninstall-dir)"
	cp "$SELF" "$case_dir/target/kiln"
	chmod 0755 "$case_dir/target/kiln"
	ln -s "$SELF" "$case_dir/fakebin/kiln"
	printf '9.9.9 full\n' >"$case_dir/fakebin/kiln.meta"
	rm -f "$case_dir/fakebin/curl"
	rm -f "$case_dir/fakebin/uname"
	for tool in basename dirname rm; do
		path="$(PATH="$BASE_PATH" command -v "$tool")"
		ln -s "$path" "$case_dir/fakebin/$tool"
	done
	status=0
	env HOME="$case_dir/home" PATH="$case_dir/fakebin" NO_COLOR=1 \
		"$TEST_SHELL" "$INSTALLER" --uninstall --yes --lite --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || status=$?
	if [ "$status" != 0 ]; then
		sed -n '1,80p' "$case_dir/output" >&2
		fail "dependency-free custom uninstall returned $status"
	fi
	[ ! -e "$case_dir/target/kiln" ] || fail "custom uninstall left the requested target"
	[ -e "$case_dir/fakebin/kiln" ] || fail "custom uninstall removed an unrelated PATH binary"
	printf 'ok - uninstalls explicit dir without install dependencies\n'
}

test_uninstall_ignores_unknown_path_binary() {
	case_dir="$(make_case uninstall-unknown)"
	ln -s "$SELF" "$case_dir/fakebin/kiln"
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" NO_COLOR=1 \
		"$TEST_SHELL" "$INSTALLER" --uninstall --yes >"$case_dir/output" 2>&1 || fail "safe default uninstall failed"
	[ -e "$case_dir/fakebin/kiln" ] || fail "default uninstall removed an unknown PATH binary"
	grep -q 'No installed kiln found.' "$case_dir/output" || fail "unknown PATH binary was treated as installer-owned"
	printf 'ok - ignores unknown PATH binary during uninstall\n'
}

test_uninstall_handles_service_without_binary() {
	case_dir="$(make_case uninstall-service)"
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" NO_COLOR=1 KILN_DRY_RUN=1 KILN_SIM_SERVICE=1 \
		"$TEST_SHELL" "$INSTALLER" --uninstall --yes >"$case_dir/output" 2>&1 || fail "service-only uninstall dry-run failed"
	grep -q 'kiln.service' "$case_dir/output" || fail "service-only uninstall exited as if nothing were installed"
	printf 'ok - handles service-only uninstall\n'
}

test_uninstall_reports_service_privilege_first() {
	case_dir="$(make_case uninstall-service-root)"
	cp "$SELF" "$case_dir/target/kiln"
	chmod 0555 "$case_dir/target"
	mkdir -p "$case_dir/root/etc/systemd/system"
	printf 'old unit\n' >"$case_dir/root/etc/systemd/system/kiln.service"
	ln -s "$SELF" "$case_dir/fakebin/id"
	status=0
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" NO_COLOR=1 KILN_TEST_ROOT="$case_dir/root" KILN_TEST_UID=1000 \
		"$TEST_SHELL" "$INSTALLER" --uninstall --yes --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || status=$?
	chmod 0755 "$case_dir/target"
	[ "$status" = 1 ] || fail "non-root service uninstall returned $status"
	grep -q 'systemd service found' "$case_dir/output" || fail "non-root uninstall did not explain service removal first"
	[ -f "$case_dir/target/kiln" ] || fail "non-root uninstall removed the binary"
	[ -f "$case_dir/root/etc/systemd/system/kiln.service" ] || fail "non-root uninstall removed the unit"
	printf 'ok - reports service privileges before binary permissions\n'
}

test_semver_order_and_exact_identity() {
	case_dir="$(make_case semver-order)"
	cp "$SELF" "$case_dir/target/kiln"
	chmod 0755 "$case_dir/target/kiln"
	printf '1.0.0-rc.2 full\n' >"$case_dir/target/kiln.meta"
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 KILN_YES=1 KILN_DRY_RUN=1 \
		"$TEST_SHELL" "$INSTALLER" --version 1.0.0-rc.10 --dir "$case_dir/target" >"$case_dir/rc-output" 2>&1 || fail "prerelease ordering failed"
	grep -q 'upgrading from v1.0.0-rc.2' "$case_dir/rc-output" || fail "rc.10 was not ordered after rc.2"
	printf '1.0.0 full\n' >"$case_dir/target/kiln.meta"
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 KILN_YES=1 KILN_DRY_RUN=1 \
		"$TEST_SHELL" "$INSTALLER" --version 1.0.0-rc.1 --dir "$case_dir/target" >"$case_dir/down-output" 2>&1 || fail "prerelease downgrade failed"
	grep -q 'downgrading from v1.0.0' "$case_dir/down-output" || fail "stable release was not ordered after its prerelease"
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 KILN_YES=1 KILN_DRY_RUN=1 \
		"$TEST_SHELL" "$INSTALLER" --version 1.0.0 --dir "$case_dir/target" >"$case_dir/same-output" 2>&1 || fail "exact identity check failed"
	grep -q 'already the latest installed version' "$case_dir/same-output" || fail "same version and variant did not exit early"
	printf 'ok - orders SemVer and skips exact identity\n'
}

test_prerelease_identifier_allows_hyphens() {
	case_dir="$(make_case prerelease-hyphen)"
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 KILN_YES=1 \
		"$TEST_SHELL" "$INSTALLER" --dry-run --version 1.0.0-alpha-beta --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || fail "valid prerelease identifier was rejected"
	grep -q 'kiln_1.0.0-alpha-beta_linux_amd64.tar.gz' "$case_dir/output" || fail "prerelease artifact was not selected"
	printf 'ok - accepts hyphens in prerelease identifiers\n'
}

test_unsupported_versions_are_rejected() {
	case_dir="$(make_case unsupported-versions)"
	long_version="1.0.0-$(printf '%0118d' 0 | tr '0' 'a')"
	for version in 1.0.0-01 1.0.0-rc..1 1.0.0+build.1 "$long_version"; do
		status=0
		env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 KILN_YES=1 \
			"$TEST_SHELL" "$INSTALLER" --dry-run --version "$version" --dir "$case_dir/target" \
			>"$case_dir/output" 2>&1 || status=$?
		[ "$status" = 1 ] || fail "unsupported version was accepted: $version"
	done
	printf 'ok - rejects unsupported version syntax\n'
}

test_semver_orders_hyphenated_identifiers() {
	case_dir="$(make_case semver-hyphen-order)"
	cp "$SELF" "$case_dir/target/kiln"
	chmod 0755 "$case_dir/target/kiln"
	printf '1.0.0-alpha-beta full\n' >"$case_dir/target/kiln.meta"
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" TMPDIR="$case_dir/tmp" NO_COLOR=1 KILN_YES=1 KILN_DRY_RUN=1 \
		"$TEST_SHELL" "$INSTALLER" --version 1.0.0-alpha-gamma --dir "$case_dir/target" \
		>"$case_dir/output" 2>&1 || fail "hyphenated prerelease comparison failed"
	grep -q 'upgrading from v1.0.0-alpha-beta' "$case_dir/output" || fail "hyphenated prerelease identifiers compared as equal"
	printf 'ok - orders hyphenated prerelease identifiers\n'
}

test_release_guard_runs_installer_contract() {
	grep -q 'Run Installer Contract' "$RELEASE_WORKFLOW" || fail "release guard does not validate versions through the installer"
	grep -q 'uses: ./.github/workflows/ci.yml' "$RELEASE_WORKFLOW" || fail "release guard does not call full CI"
	grep -q 'make test-install' "$CI_WORKFLOW" || fail "full CI does not run installer tests"
	printf 'ok - release guard runs the installer contract\n'
}

test_probe_waits_for_sources
test_failed_binary_preserves_existing_target
test_binary_directory_is_rejected
test_wrong_binary_version_preserves_existing_target
test_wrong_binary_variant_preserves_existing_target
test_stable_release_upgrades_prerelease
test_same_version_switches_variant
test_same_version_still_configures_service
test_service_upgrade_restarts_process
test_service_failure_restores_binary_and_unit
test_invalid_values_are_rejected
test_mirror_options_are_consistent
test_wget_resolves_latest_through_mirror
test_uninstall_uses_explicit_dir_without_install_dependencies
test_uninstall_ignores_unknown_path_binary
test_uninstall_handles_service_without_binary
test_uninstall_reports_service_privilege_first
test_semver_order_and_exact_identity
test_prerelease_identifier_allows_hyphens
test_unsupported_versions_are_rejected
test_semver_orders_hyphenated_identifiers
test_release_guard_runs_installer_contract
