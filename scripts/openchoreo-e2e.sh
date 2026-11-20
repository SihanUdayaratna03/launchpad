#!/usr/bin/env bash
# End-to-end check of the OpenChoreo backend on your local k3d cluster.
#
# Runs the controller on this machine (it talks to the cluster through your
# kubeconfig), sends it signed pull-request webhooks the way GitHub would, and
# checks what OpenChoreo did with them:
#
#   opened  -> Project pr-N exists, the preview URL answers 200
#   replay  -> same webhook again is accepted and changes nothing
#   closed  -> Project is gone and its data-plane namespace is cleaned up
#
# Needs: go, kubectl, curl, openssl, and an OpenChoreo cluster that is already
# running (see docs/openchoreo.md). Nothing here touches AWS or GitHub.
set -euo pipefail

PR=${PR:-9001}
PORT=${PORT:-18090}
SECRET=${SECRET:-e2e-test-secret}
SHA=${SHA:-35591a177caeeba184436994750dfc71de281258}
OC_HOST=${OC_HOST:-development-default.openchoreoapis.localhost:19080}
OC_NAMESPACE=${OC_NAMESPACE:-default}
WAIT=${WAIT:-180}

cd "$(dirname "$0")/.."

for t in go kubectl curl openssl; do
  command -v "$t" >/dev/null || { echo "missing: $t"; exit 1; }
done
kubectl get crd projects.openchoreo.dev >/dev/null 2>&1 ||
  { echo "OpenChoreo CRDs not found: is your kubeconfig pointing at the OpenChoreo cluster?"; exit 1; }

TMP=$(mktemp -d)
CTL_PID=
FAILED=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; FAILED=1; }

cleanup() {
  [ -n "$CTL_PID" ] && kill "$CTL_PID" 2>/dev/null || true
  # Leave nothing behind if the script stopped half way.
  kubectl -n "$OC_NAMESPACE" delete project "pr-$PR" --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n "$OC_NAMESPACE" delete component "launchpad-demo-pr-$PR" --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n "$OC_NAMESPACE" delete workload "launchpad-demo-pr-$PR-workload" --ignore-not-found >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

# send ACTION [SHA]: POST a signed pull_request webhook, print the HTTP status.
send() {
  local body sig
  body=$(printf '{"action":"%s","number":%s,"repository":{"full_name":"e2e/test"},"pull_request":{"title":"e2e","head":{"sha":"%s"}}}' "$1" "$PR" "${2:-$SHA}")
  sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')
  curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H "X-Hub-Signature-256: sha256=$sig" --data "$body" "http://127.0.0.1:$PORT/webhook"
}

# wait_for SECONDS COMMAND...: poll until the command succeeds.
wait_for() {
  local limit=$1 i=0; shift
  until "$@" >/dev/null 2>&1; do
    i=$((i + 5)); [ "$i" -ge "$limit" ] && return 1; sleep 5
  done
}

preview_up()   { [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 --resolve "${OC_HOST%%:*}:${OC_HOST##*:}:127.0.0.1" "http://$OC_HOST/launchpad-demo-pr-$PR-http/")" = 200 ]; }
project_gone() { ! kubectl -n "$OC_NAMESPACE" get project "pr-$PR" >/dev/null 2>&1; }
dp_ns_gone()   { ! kubectl get ns -o name | grep -q "^namespace/dp-${OC_NAMESPACE}-pr-$PR-"; }

echo "== building controller"
go build -o "$TMP/controller" ./cmd/controller

echo "== starting controller (BACKEND=openchoreo, port $PORT)"
BACKEND=openchoreo WEBHOOK_SECRET="$SECRET" PORT="$PORT" OC_NAMESPACE="$OC_NAMESPACE" OC_HOST="$OC_HOST" \
  "$TMP/controller" >"$TMP/controller.log" 2>&1 &
CTL_PID=$!
wait_for 20 curl -sf "http://127.0.0.1:$PORT/healthz" || { echo "controller did not start:"; cat "$TMP/controller.log"; exit 1; }

echo "== webhook security"
[ "$(curl -s -o /dev/null -w '%{http_code}' -X POST --data '{}' "http://127.0.0.1:$PORT/webhook")" = 401 ] \
  && pass "unsigned webhook rejected (401)" || fail "unsigned webhook was not rejected"

echo "== PR #$PR opened"
[ "$(send opened)" = 202 ] && pass "opened accepted (202)" || fail "opened was not accepted"
kubectl -n "$OC_NAMESPACE" get project "pr-$PR" >/dev/null 2>&1 \
  && pass "Project pr-$PR created" || fail "Project pr-$PR missing"
kubectl -n "$OC_NAMESPACE" get workload "launchpad-demo-pr-$PR-workload" \
  -o jsonpath='{.spec.container.image}' 2>/dev/null | grep -q ":$SHA\$" \
  && pass "Workload runs the PR's commit" || fail "Workload image is not :$SHA"

echo "== waiting up to ${WAIT}s for the preview URL"
wait_for "$WAIT" preview_up \
  && pass "http://$OC_HOST/launchpad-demo-pr-$PR-http/ answers 200" \
  || fail "preview URL did not answer 200 within ${WAIT}s"

echo "== replay"
[ "$(send opened)" = 202 ] && pass "replayed webhook accepted (202)" || fail "replay was not accepted"
grep -q "already at" "$TMP/controller.log" \
  && pass "replay changed nothing" || fail "replay was not recognised as a no-op"

echo "== PR #$PR closed"
[ "$(send closed)" = 202 ] && pass "closed accepted (202)" || fail "closed was not accepted"
wait_for "$WAIT" project_gone && pass "Project pr-$PR deleted" || fail "Project pr-$PR still exists after ${WAIT}s"
wait_for "$WAIT" dp_ns_gone \
  && pass "data-plane namespace cleaned up" || fail "data-plane namespace still present after ${WAIT}s"
[ "$(send closed)" = 202 ] && pass "closing again is harmless (202)" || fail "second close failed"

echo
if [ "$FAILED" = 0 ]; then echo "ALL CHECKS PASSED"; else echo "SOME CHECKS FAILED, controller log:"; cat "$TMP/controller.log"; fi
exit "$FAILED"
