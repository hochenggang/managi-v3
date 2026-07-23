#!/bin/sh
# install.sh 的 verify_checksum 函数测试（修复 B39）
# 运行方式：sh deploy/install_test.sh
# 注意：Windows 开发环境无法运行 sh，此测试在 Linux/CI 环境运行。

set -u

INSTALL_SH="$(dirname "$0")/install.sh"

extract_verify_checksum() {
    awk '/^verify_checksum\(\) \{/ { in_fn = 1 } in_fn { print } in_fn && /^\}$/ { in_fn = 0 }' "$INSTALL_SH"
}

PASS=0
FAIL=0

pass() { printf "  PASS: %s\n" "$1"; PASS=$((PASS + 1)); }
fail() { printf "  FAIL: %s\n" "$1"; FAIL=$((FAIL + 1)); }

# 通用测试运行器：在子 shell 中加载 stubs + verify_checksum，执行后输出状态
# 参数：$1=testfile内容 $2=asset名 $3=EXPECTED_SHA256 $4=spider返回值 $5=download返回值 $6=sidecar内容
run_case() {
    _rc_content="$1"
    _rc_asset="$2"
    _rc_expected="$3"
    _rc_spider="$4"
    _rc_download="$5"
    _rc_sidecar_content="$6"

    _rc_tmpfile="$(mktemp)"
    printf '%s' "$_rc_content" > "$_rc_tmpfile"

    (
        INFO_LOG=""
        WARN_LOG=""
        ERROR_LOG=""
        info() { INFO_LOG="${INFO_LOG}$1|"; }
        warn() { WARN_LOG="${WARN_LOG}$1|"; }
        error() { ERROR_LOG="${ERROR_LOG}$1|"; }

        wget() {
            if [ "$1" = "--spider" ]; then
                return "$_rc_spider"
            elif [ "$1" = "-qO" ]; then
                if [ "$_rc_download" -eq 0 ]; then
                    printf '%s' "$_rc_sidecar_content" > "$2"
                    return 0
                fi
                return 1
            fi
            return 1
        }

        GITHUB_REPO="test/repo"
        EXPECTED_SHA256="$_rc_expected"

        eval "$(extract_verify_checksum)"

        _vc_ret=0
        verify_checksum "$_rc_tmpfile" "$_rc_asset" || _vc_ret=$?

        printf 'RET=%d\n' "$_vc_ret"
        printf 'INFO=%s\n' "$INFO_LOG"
        printf 'WARN=%s\n' "$WARN_LOG"
        printf 'ERROR=%s\n' "$ERROR_LOG"
        printf 'FILE_EXISTS=%s\n' "$([ -f "$_rc_tmpfile" ] && echo yes || echo no)"

        rm -f "$_rc_tmpfile" 2>/dev/null
    )
}

get_field() { printf '%s' "$1" | grep "^$2=" | cut -d= -f2-; }

echo "Running verify_checksum tests..."

# 测试 1：MANAGI_SHA256 匹配 → 通过，文件保留
T1_CONTENT="hello world"
T1_SHA="$(printf '%s' "$T1_CONTENT" | sha256sum | awk '{print $1}')"
T1_OUT="$(run_case "$T1_CONTENT" "test.bin" "$T1_SHA" 1 1 "")"
T1_RET="$(get_field "$T1_OUT" RET)"
T1_INFO="$(get_field "$T1_OUT" INFO)"
T1_FILE="$(get_field "$T1_OUT" FILE_EXISTS)"
if [ "$T1_RET" = "0" ] && printf '%s' "$T1_INFO" | grep -q "环境变量" && [ "$T1_FILE" = "yes" ]; then
    pass "T1: MANAGI_SHA256 匹配时通过校验，文件保留"
else
    fail "T1: MANAGI_SHA256 匹配时通过校验 (ret=$T1_RET file=$T1_FILE info=$T1_INFO)"
fi

# 测试 2：MANAGI_SHA256 不匹配 → 失败，文件删除
T2_CONTENT="hello world"
T2_OUT="$(run_case "$T2_CONTENT" "test.bin" "0000000000000000000000000000000000000000000000000000000000000000" 1 1 "")"
T2_RET="$(get_field "$T2_OUT" RET)"
T2_ERROR="$(get_field "$T2_OUT" ERROR)"
T2_FILE="$(get_field "$T2_OUT" FILE_EXISTS)"
if [ "$T2_RET" = "1" ] && printf '%s' "$T2_ERROR" | grep -q "SHA256" && [ "$T2_FILE" = "no" ]; then
    pass "T2: MANAGI_SHA256 不匹配时报错并删除文件"
else
    fail "T2: MANAGI_SHA256 不匹配时报错并删除文件 (ret=$T2_RET file=$T2_FILE error=$T2_ERROR)"
fi

# 测试 3：无 MANAGI_SHA256，sidecar 不存在 → 警告，文件保留
T3_CONTENT="hello world"
T3_OUT="$(run_case "$T3_CONTENT" "test.bin" "" 1 1 "")"
T3_RET="$(get_field "$T3_OUT" RET)"
T3_WARN="$(get_field "$T3_OUT" WARN)"
T3_FILE="$(get_field "$T3_OUT" FILE_EXISTS)"
if [ "$T3_RET" = "0" ] && printf '%s' "$T3_WARN" | grep -q "SHA256" && [ "$T3_FILE" = "yes" ]; then
    pass "T3: 无 SHA256 且 sidecar 不存在时警告并保留文件"
else
    fail "T3: 无 SHA256 且 sidecar 不存在时警告并保留文件 (ret=$T3_RET file=$T3_FILE warn=$T3_WARN)"
fi

# 测试 4：无 MANAGI_SHA256，sidecar 存在且匹配 → 通过
T4_CONTENT="hello world"
T4_SHA="$(printf '%s' "$T4_CONTENT" | sha256sum | awk '{print $1}')"
T4_SIDECAR="${T4_SHA}  test.bin"
T4_OUT="$(run_case "$T4_CONTENT" "test.bin" "" 0 0 "$T4_SIDECAR")"
T4_RET="$(get_field "$T4_OUT" RET)"
T4_INFO="$(get_field "$T4_OUT" INFO)"
T4_FILE="$(get_field "$T4_OUT" FILE_EXISTS)"
if [ "$T4_RET" = "0" ] && printf '%s' "$T4_INFO" | grep -q "sidecar" && [ "$T4_FILE" = "yes" ]; then
    pass "T4: sidecar 存在且匹配时通过校验"
else
    fail "T4: sidecar 存在且匹配时通过校验 (ret=$T4_RET file=$T4_FILE info=$T4_INFO)"
fi

# 测试 5：无 MANAGI_SHA256，sidecar 存在但不匹配 → 失败，文件删除
T5_CONTENT="hello world"
T5_SIDECAR="0000000000000000000000000000000000000000000000000000000000000000  test.bin"
T5_OUT="$(run_case "$T5_CONTENT" "test.bin" "" 0 0 "$T5_SIDECAR")"
T5_RET="$(get_field "$T5_OUT" RET)"
T5_ERROR="$(get_field "$T5_OUT" ERROR)"
T5_FILE="$(get_field "$T5_OUT" FILE_EXISTS)"
if [ "$T5_RET" = "1" ] && printf '%s' "$T5_ERROR" | grep -q "SHA256" && [ "$T5_FILE" = "no" ]; then
    pass "T5: sidecar 存在但不匹配时报错并删除文件"
else
    fail "T5: sidecar 存在但不匹配时报错并删除文件 (ret=$T5_RET file=$T5_FILE error=$T5_ERROR)"
fi

echo ""
echo "Results: $PASS passed, $FAIL failed"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
