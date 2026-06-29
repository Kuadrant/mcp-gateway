#!/usr/bin/env bash
# suite-router.sh - centralised e2e suite selection for CI workflows
#
# inputs (env vars):
#   EVENT_TYPE       - github event (pull_request, issue_comment, schedule, workflow_dispatch)
#   PR_LABELS        - comma-separated PR labels (e.g. "e2e/discovery,e2e/sessions")
#   COMMENT_SUITE    - suite name from /test-e2e command (e.g. "discovery", "full", "pr-extended")
#   DISPATCH_SUITE   - suite from workflow_dispatch input
#   CHANGED_FILES    - newline-separated list of changed files (for suggestions only)
#
# outputs (written to $GITHUB_OUTPUT or stdout):
#   suites           - JSON array of suite names to run (e.g. ["core","sessions"])
#   make_target      - make target to run (e.g. "test-e2e-suite SUITE=core")
#   suggestions      - comma-separated suggested suites based on changed files
#   is_full          - "true" if full run requested

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KNOWN_SUITES_FILE="$SCRIPT_DIR/known-suites.txt"

is_known_suite() {
  grep -Fxq "$1" "$KNOWN_SUITES_FILE"
}

# suites that need extra infrastructure
INFRA_SUITES="auth-policy tls multi-gateway"

suites=()
suggestions=()
is_full=false

# parse labels into suite requests
parse_labels() {
  local IFS=','
  for label in $PR_LABELS; do
    label=$(echo "$label" | xargs)
    case "$label" in
      e2e/full)
        is_full=true
        ;;
      e2e/pr-extended)
        suites+=("pr-extended")
        ;;
      e2e/*)
        suite="${label#e2e/}"
        if is_known_suite "$suite"; then
          suites+=("$suite")
        fi
        ;;
    esac
  done
}

# parse comment or dispatch suite
parse_suite_input() {
  local input="$1"
  case "$input" in
    full)
      is_full=true
      ;;
    pr-extended)
      suites+=("pr-extended")
      ;;
    pr|happy|"")
      # no additional suites
      ;;
    *)
      if is_known_suite "$input"; then
        suites+=("$input")
      fi
      ;;
  esac
}

# infer suggestions from changed files (never used to widen the gate)
infer_suggestions() {
  [ -z "${CHANGED_FILES:-}" ] && return
  while IFS= read -r f; do
    case "$f" in
      internal/router/*|internal/ext_proc/*)
        suggestions+=(routing sessions security)
        ;;
      internal/broker/upstream/tls*)
        suggestions+=(tls)
        ;;
      internal/broker/upstream/*|internal/broker/broker.go)
        suggestions+=(core discovery prompts)
        ;;
      internal/broker/session*|internal/router/session*|internal/broker/jwt*)
        suggestions+=(sessions security)
        ;;
      internal/broker/auth*|internal/router/auth*)
        suggestions+=(auth-policy trusted-headers)
        ;;
      internal/broker/elicit*|internal/router/elicit*)
        suggestions+=(url-elicitation elicitation)
        ;;
      internal/controller/*|api/*)
        suggestions+=(routing multi-gateway)
        ;;
      internal/broker/tls*)
        suggestions+=(tls)
        ;;
      tests/e2e/*)
        # suggest the suite for the changed test file
        case "$f" in
          *discovery*) suggestions+=(discovery) ;;
          *auth_policy*) suggestions+=(auth-policy) ;;
          *multi_gateway*) suggestions+=(multi-gateway) ;;
          *tls*|*custom_tls*) suggestions+=(tls) ;;
          *url_elicitation*) suggestions+=(url-elicitation) ;;
          *elicitation*) suggestions+=(elicitation) ;;
          *user_specific*) suggestions+=(user-specific-list) ;;
          *happy_path*) suggestions+=(core sessions prompts routing trusted-headers) ;;
        esac
        ;;
    esac
  done <<< "$CHANGED_FILES"

  # deduplicate
  mapfile -t suggestions < <(printf '%s\n' "${suggestions[@]}" | sort -u)
}

# main routing logic
route() {
  case "${EVENT_TYPE:-}" in
    schedule)
      is_full=true
      ;;
    workflow_dispatch)
      parse_suite_input "${DISPATCH_SUITE:-}"
      ;;
    issue_comment)
      parse_suite_input "${COMMENT_SUITE:-}"
      ;;
    pull_request|pull_request_target)
      parse_labels
      ;;
  esac

  infer_suggestions

  # deduplicate suites
  if [ ${#suites[@]} -gt 0 ]; then
    mapfile -t suites < <(printf '%s\n' "${suites[@]}" | sort -u)
  fi
}

# build make target string
build_target() {
  if [ "$is_full" = "true" ]; then
    echo "test-e2e-ci-full"
    return
  fi

  if [ ${#suites[@]} -eq 0 ]; then
    echo "test-e2e-pr"
    return
  fi

  # pr-extended alone uses its own target; mixed with named suites
  # means both need to run, so caller iterates the suites array
  local has_extended=false has_named=false
  [[ " ${suites[*]} " == *" pr-extended "* ]] && has_extended=true
  for s in "${suites[@]}"; do [ "$s" != "pr-extended" ] && { has_named=true; break; }; done

  if [ "$has_extended" = "true" ] && [ "$has_named" = "false" ]; then
    echo "test-e2e-pr-extended"
    return
  fi

  # individual suites (possibly including pr-extended) - caller iterates
  echo "test-e2e-suite"
}

# output results
emit() {
  local target
  target=$(build_target)

  # build JSON array of suites
  local json
  if [ "$is_full" = "true" ]; then
    json='["full"]'
  elif [ ${#suites[@]} -eq 0 ]; then
    json='["pr"]'
  else
    json=$(printf ',"%s"' "${suites[@]}")
    json="[${json:1}]"
  fi

  local suggestions_json=""
  if [ ${#suggestions[@]} -gt 0 ]; then
    suggestions_json=$(IFS=','; echo "${suggestions[*]}")
  fi

  local out="${GITHUB_OUTPUT:-}"
  _write() {
    echo "$1"
    [ -n "$out" ] && echo "$1" >> "$out" || true
  }
  _write "suites=$json"
  _write "make_target=$target"
  _write "suggestions=$suggestions_json"
  _write "is_full=$is_full"
}

route
emit
