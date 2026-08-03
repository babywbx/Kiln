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
		printf 'https://github.com/babywbx/kiln/releases/tag/v%s' "$KILN_TEST_RELEASE_VERSION"
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

fake_binary() {
	version="${KILN_TEST_RELEASE_VERSION:-1.0.0}"
	variant="${KILN_TEST_RELEASE_VARIANT:-full}"
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
kiln | kiln-lite | .kiln.new.*) fake_binary "$@"; exit ;;
esac

HERE="$(dirname -- "$SELF")"
INSTALLER="$HERE/../install.sh"
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
	printf 'ok 1 - waits for concurrent source probes\n'
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
	printf 'ok 2 - preserves existing target when staged binary fails\n'
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
	printf 'ok 3 - stable release upgrades prerelease\n'
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
	printf 'ok 4 - same version switches variant\n'
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
	printf 'ok 5 - rejects missing and unsafe option values\n'
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
	printf 'ok 6 - keeps mirror options secure and consistent\n'
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
	grep -q '^https://mirror.example/https://api.github.com/repos/babywbx/kiln/releases/latest$' "$case_dir/requests" || fail "wget resolved latest outside the manual mirror"
	[ -x "$case_dir/target/kiln" ] || fail "wget mirror install did not create the target binary"
	printf 'ok 7 - wget resolves latest through manual mirror\n'
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
	printf 'ok 8 - uninstalls explicit dir without install dependencies\n'
}

test_uninstall_ignores_unknown_path_binary() {
	case_dir="$(make_case uninstall-unknown)"
	ln -s "$SELF" "$case_dir/fakebin/kiln"
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" NO_COLOR=1 \
		"$TEST_SHELL" "$INSTALLER" --uninstall --yes >"$case_dir/output" 2>&1 || fail "safe default uninstall failed"
	[ -e "$case_dir/fakebin/kiln" ] || fail "default uninstall removed an unknown PATH binary"
	grep -q 'No installed kiln found.' "$case_dir/output" || fail "unknown PATH binary was treated as installer-owned"
	printf 'ok 9 - ignores unknown PATH binary during uninstall\n'
}

test_uninstall_handles_service_without_binary() {
	case_dir="$(make_case uninstall-service)"
	env HOME="$case_dir/home" PATH="$case_dir/fakebin:$BASE_PATH" NO_COLOR=1 KILN_DRY_RUN=1 KILN_SIM_SERVICE=1 \
		"$TEST_SHELL" "$INSTALLER" --uninstall --yes >"$case_dir/output" 2>&1 || fail "service-only uninstall dry-run failed"
	grep -q 'kiln.service' "$case_dir/output" || fail "service-only uninstall exited as if nothing were installed"
	printf 'ok 10 - handles service-only uninstall\n'
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
	printf 'ok 11 - orders SemVer and skips exact identity\n'
}

test_probe_waits_for_sources
test_failed_binary_preserves_existing_target
test_stable_release_upgrades_prerelease
test_same_version_switches_variant
test_invalid_values_are_rejected
test_mirror_options_are_consistent
test_wget_resolves_latest_through_mirror
test_uninstall_uses_explicit_dir_without_install_dependencies
test_uninstall_ignores_unknown_path_binary
test_uninstall_handles_service_without_binary
test_semver_order_and_exact_identity
