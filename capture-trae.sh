#!/bin/bash
# Trae IDE 네트워크 캡처 스크립트
# 사용법: ./capture-trae.sh

echo "=== Trae IDE 네트워크 캡처 ==="
echo ""

# 1. mitmproxy 시작 (백그라운드)
echo "[1/3] mitmproxy 시작 중... (포트 8080)"
echo "      웹 UI: http://localhost:8081"
mitmweb --listen-port 8080 --web-port 8081 --set console_eventlog_verbosity=debug &
MITM_PID=$!
sleep 2

echo "[2/3] 환경변수 설정"
export HTTP_PROXY=http://127.0.0.1:8080
export HTTPS_PROXY=http://127.0.0.1:8080
export NODE_TLS_REJECT_UNAUTHORIZED=0
export ELECTRON_ENABLE_LOGGING=1

echo "[3/3] Trae IDE 실행"
echo ""
echo "=========================================="
echo "지금 Trae IDE를 다음 명령으로 실행하세요:"
echo ""
echo "  HTTP_PROXY=http://127.0.0.1:8080 \\"
echo "  HTTPS_PROXY=http://127.0.0.1:8080 \\"
echo "  NODE_TLS_REJECT_UNAUTHORIZED=0 \\"
echo "  /path/to/trae"
echo ""
echo "또는 AppImage인 경우:"
echo "  HTTP_PROXY=http://127.0.0.1:8080 \\"
echo "  HTTPS_PROXY=http://127.0.0.1:8080 \\"
echo "  NODE_TLS_REJECT_UNAUTHORIZED=0 \\"
echo "  ~/Trae*.AppImage"
echo ""
echo "=========================================="
echo ""
echo "캡처된 요청은 http://localhost:8081 에서 확인하세요"
echo "종료하려면 Ctrl+C"
echo ""

# mitmproxy 프로세스 대기
wait $MITM_PID
