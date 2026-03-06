#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${OPENCODEROUTER_PORT:-8080}"
WORK_ROOT="${OPENCODEROUTER_WORK_ROOT:-/tmp/opencoderouter-dev}"
WS1="${WORK_ROOT}/workspace-a"
WS2="${WORK_ROOT}/workspace-b"

mkdir -p "${WS1}" "${WS2}"

touch "${WS1}/README.md" "${WS2}/README.md"

echo "[dev-setup] root: ${ROOT_DIR}"
echo "[dev-setup] control-plane port: ${PORT}"
echo "[dev-setup] workspace A: ${WS1}"
echo "[dev-setup] workspace B: ${WS2}"

cleanup() {
  local exit_code=$?
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "${SERVER_PID}" 2>/dev/null; then
    echo "[dev-setup] stopping control plane (pid=${SERVER_PID})"
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  if [[ -n "${TAIL_PID:-}" ]] && kill -0 "${TAIL_PID}" 2>/dev/null; then
    kill "${TAIL_PID}" 2>/dev/null || true
    wait "${TAIL_PID}" 2>/dev/null || true
  fi
  exit "${exit_code}"
}

trap cleanup EXIT INT TERM

LOG_FILE="${WORK_ROOT}/control-plane.log"

echo "[dev-setup] starting control plane..."
(cd "${ROOT_DIR}" && go run . -port "${PORT}" "${WS1}" "${WS2}") >"${LOG_FILE}" 2>&1 &
SERVER_PID=$!

for _ in {1..40}; do
  if curl -fsS "http://127.0.0.1:${PORT}/api/health" >/dev/null 2>&1; then
    echo "[dev-setup] control plane is healthy"
    break
  fi
  sleep 0.25
done

echo
echo "OpenCodeRouter dev setup is ready"
echo "--------------------------------"
echo "Dashboard:   http://127.0.0.1:${PORT}/"
echo "Sessions:    http://127.0.0.1:${PORT}/api/sessions"
echo "Events SSE:  http://127.0.0.1:${PORT}/api/events"
echo "Terminal WS: ws://127.0.0.1:${PORT}/ws/terminal/{session-id}"
echo
echo "Useful commands:"
echo "  curl -s http://127.0.0.1:${PORT}/api/sessions | jq"
echo "  curl -N http://127.0.0.1:${PORT}/api/events"
echo "  tail -f ${LOG_FILE}"
echo
echo "Press Ctrl+C to stop everything."

tail -f "${LOG_FILE}" &
TAIL_PID=$!
wait "${SERVER_PID}"
