# Trae IDE API 조사 결과

## 발견한 정보 요약

### 1. trae2api 프로젝트 분석
- **프로젝트 상태**: 2025년 8월 11일 아카이브됨 (읽기 전용)
- **최종 버전**: v1.1.3 (2025년 6월 6일)
- **중요 제한사항**: Trae v1.3.0 이후 새 모델 미지원

### 2. 실제 API 엔드포인트 발견
**기본 URL들:**
- `https://trae-api-sg.mchost.guru` (기본 API)
- `https://api-sg-central.trae.ai` (토큰 갱신)
- `https://imagex-ap-singapore-1.bytevcloudapi.com` (파일 ID 획득)
- `https://tos-sg16-share.vodupload.com` (파일 업로드)

**핵심 엔드포인트:**
- 토큰 갱신: `POST /cloudide/api/v3/trae/oauth/ExchangeToken`
- 채팅 완료: `POST /v1/chat/completions` (OpenAI 호환)

### 3. 인증 메커니즘
**필수 환경변수:**
- `APP_ID`: Trae 애플리케이션 ID
- `CLIENT_ID`: OAuth 클라이언트 ID  
- `REFRESH_TOKEN`: OAuth 리프레시 토큰
- `USER_ID`: 사용자 ID
- `AUTH_TOKEN`: API 접근 인증 토큰

**토큰 갱신 프로세스:**
1. RefreshToken으로 새 RefreshToken 획득
2. 새 RefreshToken으로 AccessToken 획득
3. 5분마다 자동 갱신

### 4. "param is invalid" 오류 원인
- 프로젝트가 아카이브되어 더 이상 업데이트되지 않음
- Trae v1.3.0 이후 API 변경사항 미반영
- 환경변수 누락 또는 잘못된 설정

## 실제 작동하는 curl 명령 예시

### 토큰 갱신
```bash
curl -X POST "https://api-sg-central.trae.ai/cloudide/api/v3/trae/oauth/ExchangeToken" \
  -H "Content-Type: application/json" \
  -d '{
    "ClientID": "your_client_id",
    "RefreshToken": "your_refresh_token", 
    "ClientSecret": "-",
    "UserID": "your_user_id"
  }'
```

### 채팅 완료 (토큰 획득 후)
```bash
curl -X POST "https://trae-api-sg.mchost.guru/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your_access_token" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {
        "role": "user", 
        "content": "안녕하세요"
      }
    ],
    "stream": false
  }'
```

## 네트워크 캡처 대안 방법

### 1. Electron 앱 네트워크 캡처
**HTTP Toolkit 사용:**
- Electron 앱 자동 설정 지원
- Main/Renderer 프로세스 모두 캡처
- HTTPS 트래픽 복호화 가능

**mitmproxy 사용:**
```bash
# 환경변수 설정
export SSLKEYLOGFILE=$PWD/keylogfile.txt

# mitmproxy 시작 (포트 8080)
mitmproxy

# Electron 앱을 프록시 통해 실행
HTTPS_PROXY=http://localhost:8080 HTTP_PROXY=http://localhost:8080 /path/to/trae
```

### 2. Wireshark + mitmproxy 조합
```bash
# TLS 키 로그 파일 생성
export SSLKEYLOGFILE=$PWD/keylogfile.txt

# mitmproxy 시작
mitmproxy

# Wireshark에서 키 로그 파일 설정
# Edit > Preferences > Protocols > TLS > (Pre)-Master-Secret log filename
```

### 3. Electron DevTools 활용
- Main 프로세스 디버깅: `--inspect=9229` 플래그
- Renderer 프로세스: 기본 DevTools Network 탭
- 모든 프로세스 캡처: `--enable-logging --log-level=0`

## 추가 발견사항

### 보안 관련
- ByteDance의 광범위한 데이터 수집 시스템 존재
- 싱가포르 기반 SPRING(SG)PTE.LTD.에서 운영
- 텔레메트리 및 추적 기능 내장

### API 제한사항
- 무료 사용자 요청 제한 존재
- "Too many current requests" 오류 빈발
- Pro 계정도 일부 기능 제한

### 대안 도구
- Cursor IDE의 리버스 엔지니어링 프로젝트 존재
- eisbaw/cursor_api_demo 참조 가능
