#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SCENARIO_DOC="$SCRIPT_DIR/release-e2e-scenarios.md"

TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-90}"
KEEP_GOING=1
LIST_ONLY=0
KEEP_ARTIFACTS=0
FROM_ID=""
TO_ID=""
MATCH_PATTERN=""
SCENARIO_FILTER=""
QA_SUFFIX="${QA_SUFFIX:-$(date +%s)-$$}"
OUTPUT_DIR="${OUTPUT_DIR:-}"
OUTPUT_DIR_SET=0

usage() {
  cat <<EOF
Usage: tests/e2e/run-release-e2e.sh [options]

Runs the bash scenarios documented in tests/e2e/release-e2e-scenarios.md.

Options:
  --list                 List scenarios and exit
  --scenario IDS         Run only specific scenarios, comma-separated (e.g. S54,S55)
  --from ID              Run scenarios starting at ID
  --to ID                Run scenarios ending at ID
  --match REGEX          Run scenarios whose ID or title matches REGEX
  --timeout SECONDS      Per-scenario timeout in seconds (default: 90)
  --qa-suffix VALUE      Reuse a specific QA suffix
  --output-dir DIR       Write logs and runner artifacts to DIR
  --stop-on-failure      Stop after the first failing scenario
  --keep-artifacts       Keep auth state artifacts under the output dir
  --help                 Show this help

Examples:
  tests/e2e/run-release-e2e.sh
  tests/e2e/run-release-e2e.sh --list
  tests/e2e/run-release-e2e.sh --from S54 --to S58
  tests/e2e/run-release-e2e.sh --scenario S61,S62,S70 --keep-artifacts
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

contains_csv_value() {
  local needle="$1"
  local csv="$2"
  local item

  if [[ -z "$csv" ]]; then
    return 1
  fi

  local old_ifs="$IFS"
  IFS=','
  for item in $csv; do
    item="${item#"${item%%[![:space:]]*}"}"
    item="${item%"${item##*[![:space:]]}"}"
    if [[ "$item" == "$needle" ]]; then
      IFS="$old_ifs"
      return 0
    fi
  done
  IFS="$old_ifs"
  return 1
}

matches_pattern() {
  local text="$1"
  local pattern="$2"

  if [[ -z "$pattern" ]]; then
    return 0
  fi

  printf '%s\n' "$text" | grep -Eqi -- "$pattern"
}

find_index_by_id() {
  local needle="$1"
  local i

  for ((i = 0; i < ${#SCENARIO_IDS[@]}; i++)); do
    if [[ "${SCENARIO_IDS[$i]}" == "$needle" ]]; then
      printf '%s\n' "$i"
      return 0
    fi
  done

  return 1
}

cleanup_release_auth_artifacts() {
  rm -f \
    "$OUTPUT_DIR/qa-release-auth-key.json" \
    "$OUTPUT_DIR/qa-release-auth-key.token" \
    "$OUTPUT_DIR/qa-release-workflow.json" \
    "$OUTPUT_DIR/qa-release-workflow.id"
}

run_with_timeout() {
  local script_path="$1"
  local stdout_path="$2"
  local stderr_path="$3"
  local timeout_marker="$4"

  rm -f "$timeout_marker"

  bash "$script_path" >"$stdout_path" 2>"$stderr_path" &
  local pid=$!

  (
    sleep "$TIMEOUT_SECONDS"
    if kill -0 "$pid" 2>/dev/null; then
      : >"$timeout_marker"
      kill -TERM "$pid" 2>/dev/null || true
      sleep 1
      kill -KILL "$pid" 2>/dev/null || true
    fi
  ) &
  local watchdog=$!

  local status=0
  if ! wait "$pid"; then
    status=$?
  fi

  kill "$watchdog" 2>/dev/null || true
  wait "$watchdog" 2>/dev/null || true

  return "$status"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --list)
      LIST_ONLY=1
      shift
      ;;
    --scenario)
      [[ $# -ge 2 ]] || die "--scenario requires a value"
      SCENARIO_FILTER="$2"
      shift 2
      ;;
    --from)
      [[ $# -ge 2 ]] || die "--from requires a value"
      FROM_ID="$2"
      shift 2
      ;;
    --to)
      [[ $# -ge 2 ]] || die "--to requires a value"
      TO_ID="$2"
      shift 2
      ;;
    --match)
      [[ $# -ge 2 ]] || die "--match requires a value"
      MATCH_PATTERN="$2"
      shift 2
      ;;
    --timeout)
      [[ $# -ge 2 ]] || die "--timeout requires a value"
      TIMEOUT_SECONDS="$2"
      shift 2
      ;;
    --qa-suffix)
      [[ $# -ge 2 ]] || die "--qa-suffix requires a value"
      QA_SUFFIX="$2"
      shift 2
      ;;
    --output-dir)
      [[ $# -ge 2 ]] || die "--output-dir requires a value"
      OUTPUT_DIR="$2"
      OUTPUT_DIR_SET=1
      shift 2
      ;;
    --stop-on-failure)
      KEEP_GOING=0
      shift
      ;;
    --keep-artifacts)
      KEEP_ARTIFACTS=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

[[ "$TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || die "--timeout must be an integer number of seconds"
[[ -f "$SCENARIO_DOC" ]] || die "missing scenario file: $SCENARIO_DOC"

if (( OUTPUT_DIR_SET == 0 )); then
  OUTPUT_DIR="/tmp/gomodel-release-e2e-$QA_SUFFIX"
fi

for tool in awk bash curl grep jq mktemp sed; do
  command -v "$tool" >/dev/null 2>&1 || die "required tool not found: $tool"
done

mkdir -p "$OUTPUT_DIR"
PARSED_DIR="$OUTPUT_DIR/parsed"
RUN_DIR="$OUTPUT_DIR/run"
mkdir -p "$PARSED_DIR" "$RUN_DIR"

MANIFEST="$PARSED_DIR/scenarios.tsv"
SETUP_MANIFEST="$PARSED_DIR/setups.tsv"
: >"$MANIFEST"
: >"$SETUP_MANIFEST"

awk -v parsed_dir="$PARSED_DIR" -v manifest="$MANIFEST" -v setup_manifest="$SETUP_MANIFEST" '
  function flush_code_block(    path) {
    if (current_id != "") {
      path = sprintf("%s/%s.sh", parsed_dir, current_id)
      print code > path
      close(path)
      printf "%s\t%s\t%s\n", current_id, current_title, path >> manifest
      close(manifest)
      current_id = ""
      current_title = ""
    } else if (current_h2 == "Common environment" || current_h2 == "Auth-enabled runtime environment") {
      setup_count++
      path = sprintf("%s/setup-%02d.sh", parsed_dir, setup_count)
      print code > path
      close(path)
      print path >> setup_manifest
      close(setup_manifest)
    }
    code = ""
  }

  {
    sub(/\r$/, "", $0)
    if (!in_code && $0 ~ /^## /) {
      current_h2 = substr($0, 4)
    }
    if (!in_code && $0 ~ /^### S[0-9][0-9] /) {
      current_id = $2
      current_title = substr($0, length(current_id) + 5)
      next
    }

    if ($0 == "```bash") {
      in_code = 1
      code = ""
      next
    }

    if (in_code && $0 == "```") {
      in_code = 0
      flush_code_block()
      next
    }

    if (in_code) {
      code = code $0 ORS
    }
  }
' "$SCENARIO_DOC"

SETUP_FILES=()
while IFS= read -r path; do
  if [[ -n "$path" ]]; then
    SETUP_FILES+=("$path")
  fi
done <"$SETUP_MANIFEST"

SCENARIO_IDS=()
SCENARIO_TITLES=()
SCENARIO_FILES=()
while IFS=$'\t' read -r id title path; do
  if [[ -n "$id" ]]; then
    SCENARIO_IDS+=("$id")
    SCENARIO_TITLES+=("$title")
    SCENARIO_FILES+=("$path")
  fi
done <"$MANIFEST"

if [[ ${#SCENARIO_IDS[@]} -eq 0 ]]; then
  die "no scenarios were parsed from $SCENARIO_DOC"
fi

START_INDEX=0
END_INDEX=$((${#SCENARIO_IDS[@]} - 1))

if [[ -n "$FROM_ID" ]]; then
  START_INDEX="$(find_index_by_id "$FROM_ID")" || die "unknown --from scenario: $FROM_ID"
fi
if [[ -n "$TO_ID" ]]; then
  END_INDEX="$(find_index_by_id "$TO_ID")" || die "unknown --to scenario: $TO_ID"
fi
if (( START_INDEX > END_INDEX )); then
  die "--from must not come after --to"
fi

SELECTED_INDEXES=()
for ((i = 0; i < ${#SCENARIO_IDS[@]}; i++)); do
  if (( i < START_INDEX || i > END_INDEX )); then
    continue
  fi
  if [[ -n "$SCENARIO_FILTER" ]] && ! contains_csv_value "${SCENARIO_IDS[$i]}" "$SCENARIO_FILTER"; then
    continue
  fi
  if ! matches_pattern "${SCENARIO_IDS[$i]} ${SCENARIO_TITLES[$i]}" "$MATCH_PATTERN"; then
    continue
  fi
  SELECTED_INDEXES+=("$i")
done

if [[ ${#SELECTED_INDEXES[@]} -eq 0 ]]; then
  die "no scenarios matched the selection"
fi

if (( LIST_ONLY == 1 )); then
  for index in "${SELECTED_INDEXES[@]}"; do
    printf '%s\t%s\n' "${SCENARIO_IDS[$index]}" "${SCENARIO_TITLES[$index]}"
  done
  exit 0
fi

RAW_LOG="$OUTPUT_DIR/release-e2e.raw.log"
SUMMARY_LOG="$OUTPUT_DIR/release-e2e.summary.tsv"

cleanup_release_auth_artifacts
if (( KEEP_ARTIFACTS == 0 )); then
  trap cleanup_release_auth_artifacts EXIT
fi

{
  printf 'id\ttitle\texit_code\ttimed_out\tduration_s\tstdout_bytes\tstderr_bytes\n'
} >"$SUMMARY_LOG"

{
  printf '# Release E2E raw log\n'
  printf '# source=%s\n' "$SCENARIO_DOC"
  printf '# qa_suffix=%s\n' "$QA_SUFFIX"
  printf '# output_dir=%s\n' "$OUTPUT_DIR"
  printf '\n'
} >"$RAW_LOG"

FAILURES=0

for index in "${SELECTED_INDEXES[@]}"; do
  scenario_id="${SCENARIO_IDS[$index]}"
  scenario_title="${SCENARIO_TITLES[$index]}"
  scenario_source="${SCENARIO_FILES[$index]}"

  scenario_script="$RUN_DIR/${scenario_id}.sh"
  stdout_log="$RUN_DIR/${scenario_id}.stdout.log"
  stderr_log="$RUN_DIR/${scenario_id}.stderr.log"
  timeout_marker="$RUN_DIR/${scenario_id}.timed_out"

  {
    printf '#!/usr/bin/env bash\n'
    printf 'set -euo pipefail\n'
    printf 'shopt -s inherit_errexit 2>/dev/null || true\n'
    printf 'cd %q\n' "$REPO_ROOT"
    printf 'export QA_SUFFIX=%q\n' "$QA_SUFFIX"
    printf 'export QA_RUN_DIR=%q\n' "$OUTPUT_DIR"
    printf 'export RUN_RELEASE_E2E_PERSIST_STATE=1\n'
    for setup_file in "${SETUP_FILES[@]}"; do
      cat "$setup_file"
      printf '\n'
    done
    cat "$scenario_source"
  } >"$scenario_script"
  chmod +x "$scenario_script"

  started_at="$(date +%s)"
  if run_with_timeout "$scenario_script" "$stdout_log" "$stderr_log" "$timeout_marker"; then
    exit_code=0
  else
    exit_code=$?
  fi
  ended_at="$(date +%s)"
  duration_s=$((ended_at - started_at))

  timed_out="false"
  if [[ -f "$timeout_marker" ]]; then
    timed_out="true"
  fi

  stdout_bytes="$(wc -c <"$stdout_log" | tr -d ' ')"
  stderr_bytes="$(wc -c <"$stderr_log" | tr -d ' ')"

  {
    printf '## %s %s\n' "$scenario_id" "$scenario_title"
    printf 'exit_code: %s\n' "$exit_code"
    printf 'timed_out: %s\n' "$timed_out"
    printf 'duration_s: %s\n' "$duration_s"
    printf -- '-- stdout --\n'
    cat "$stdout_log"
    [[ -s "$stdout_log" ]] && tail -c1 "$stdout_log" | grep -q $'\n' || printf '\n'
    printf -- '-- stderr --\n'
    cat "$stderr_log"
    [[ -s "$stderr_log" ]] && tail -c1 "$stderr_log" | grep -q $'\n' || printf '\n'
    printf '\n'
  } >>"$RAW_LOG"

  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$scenario_id" \
    "$scenario_title" \
    "$exit_code" \
    "$timed_out" \
    "$duration_s" \
    "$stdout_bytes" \
    "$stderr_bytes" >>"$SUMMARY_LOG"

  if [[ "$timed_out" == "true" ]]; then
    printf '%s TIMEOUT %ss %s\n' "$scenario_id" "$duration_s" "$scenario_title"
    FAILURES=$((FAILURES + 1))
  elif [[ "$exit_code" -eq 0 ]]; then
    printf '%s OK %ss %s\n' "$scenario_id" "$duration_s" "$scenario_title"
  else
    printf '%s EXIT%s %ss %s\n' "$scenario_id" "$exit_code" "$duration_s" "$scenario_title"
    FAILURES=$((FAILURES + 1))
  fi

  if (( FAILURES > 0 && KEEP_GOING == 0 )); then
    break
  fi
done

printf 'RAW_LOG=%s\n' "$RAW_LOG"
printf 'SUMMARY_LOG=%s\n' "$SUMMARY_LOG"
printf 'QA_SUFFIX=%s\n' "$QA_SUFFIX"

if (( FAILURES > 0 )); then
  exit 1
fi
