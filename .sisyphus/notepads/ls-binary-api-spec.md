# LS Binary API Specification

> **Research Date**: 2026-02-20  
> **LS Binary Version**: 1.18.3-4739469533380608 (linux-x64)  
> **Source**: Google Antigravity Language Server

## 1. 개요

LS 바이너리(`language_server_linux_x64`)는 Google의 Antigravity IDE에서 사용하는 Language Server Protocol(LSP) 기반 백엔드 바이너리입니다. 이 바이너리는 BoringSSL을 사용하여 JA3/JA4 핑거프린팅을 회피하며, 로컬 HTTP API 서버를 통해 AI 모델 추론 기능을 제공합니다.

## 2. 바이너리 다운로드

### 2.1 Google CDN (공식)

```bash
# Linux x64
curl -fsSL 'https://edgedl.me.gvt1.com/edgedl/release2/j0qc3/antigravity/stable/1.18.3-4739469533380608/linux-x64/Antigravity.tar.gz' \
  -o antigravity.tar.gz

# Linux ARM64  
curl -fsSL 'https://edgedl.me.gvt1.com/edgedl/release2/j0qc3/antigravity/stable/1.18.3-4739469533380608/linux-arm/Antigravity.tar.gz' \
  -o antigravity.tar.gz
```

### 2.2 추출 경로

```bash
tar xzf antigravity.tar.gz \
  -C /path/to/extract \
  --strip-components=0 \
  'Antigravity/resources/app/extensions/antigravity/bin/language_server_linux_x64'

# 결과 경로
./Antigravity/resources/app/extensions/antigravity/bin/language_server_linux_x64
```

## 3. 실행 방법

### 3.1 CLI 인자

| 인자 | 기본값 | 설명 |
|------|--------|------|
| `-api_server_url` | `http://0.0.0.0:50001` | API 서버 호스트/포트 |
| `-server_port` | `42100` | Language Server가 확장 프로그램과 통신할 포트 (HTTPS) |
| `-standalone` | `false` | 독립 실행 모드 활성화 |
| `-gemini_dir` | `.gemini` | Gemini 파일 저장 경로 (상대 경로) |
| `-app_data_dir` | `antigravity` | 애플리케이션 데이터 저장 경로 (상대 경로) |
| `-lsp_port` | `42101` | LSP 프로토콜 포트 |
| `-cli` | `false` | CLI 모드로 실행 |
| `-local_chrome_headless` | `true` | Chrome headless 모드 사용 |
| `-enable_lsp` | `false` | LSP 활성화 |

### 3.2 실행 예시

```bash
# standalone 모드로 실행 (ZeroGravity 방식)
./language_server_linux_x64 \
  -standalone=true \
  -api_server_url="http://127.0.0.1:8742" \
  -gemini_dir=./gemini \
  -app_data_dir=./data

# 출력 예시:
# Language server listening on fixed port at 42100 for HTTPS
# Language server listening on fixed port at 39251 for HTTP
```

### 3.3 환경 변수

| 변수 | 기본값 | 설명 |
|------|--------|------|
| `ZEROGRAVITY_LS_PATH` | Auto-detected | LS 바이너리 경로 |
| `ZEROGRAVITY_DATA_DIR` | `/tmp/.agcache` | Standalone LS 데이터 디렉토리 |
| `ZEROGRAVITY_APP_ROOT` | Auto-detected | Antigravity 앱 루트 디렉토리 |
| `ZEROGRAVITY_LS_USER` | `zerogravity-ls` | UID-scoped LS isolation용 시스템 유저 |

## 4. API 엔드포인트

### 4.1 서버 포트

LS 바이너리는 두 개의 포트를 노출합니다:

1. **HTTPS 포트**: `-server_port` (기본: 42100) - LSP/Extension 통신용
2. **HTTP 포트**: 랜덤 또는 고정 포트 - API 서버용 (`-api_server_url`로 설정)

> **중요**: ZeroGravity는 LS 바이너리를 `127.0.0.1:8742`에서 실행합니다.

### 4.2 주요 API 메서드

#### 4.2.1 SendUserCascadeMessage (채팅 생성)

스트리밍 및 비스트리밍 채팅 완성을 생성합니다.

**요청 형식**:
```json
{
  "jsonrpc": "2.0",
  "method": "SendUserCascadeMessage",
  "params": {
    "metadata": {
      "apiKey": "ya29.a0AXooCgt...",
      "projectId": "optional-project-id"
    },
    "request": {
      "model": "claude-3-opus",
      "messages": [
        {
          "role": "user",
          "content": "Hello!"
        }
      ],
      "generationConfig": {
        "temperature": 0.7,
        "maxOutputTokens": 4096
      },
      "systemInstruction": {
        "parts": [
          {
            "text": "System instruction here"
          }
        ]
      }
    }
  },
  "id": 1
}
```

**응답 형식 (스트리밍)**:
```json
{
  "jsonrpc": "2.0",
  "result": {
    "candidates": [
      {
        "content": {
          "parts": [
            {
              "text": "chunk of response..."
            }
          ]
        },
        "finishReason": "STOP"
      }
    ]
  },
  "id": 1
}
```

#### 4.2.2 GetCommandModelConfigs (모델 목록)

사용 가능한 모델 목록을 조회합니다.

**요청 형식**:
```json
{
  "jsonrpc": "2.0",
  "method": "GetCommandModelConfigs",
  "params": {
    "metadata": {
      "apiKey": "ya29.a0AXooCgt..."
    }
  },
  "id": 1
}
```

**응답 형식**:
```json
{
  "jsonrpc": "2.0",
  "result": {
    "models": [
      {
        "name": "claude-3-opus",
        "displayName": "Claude 3 Opus",
        "capabilities": ["streaming", "function_calling"]
      },
      {
        "name": "gemini-3-pro",
        "displayName": "Gemini 3 Pro",
        "capabilities": ["streaming"]
      }
    ]
  },
  "id": 1
}
```

## 5. 토큰 전달 방식

### 5.1 LS Binary API (정확히 확인됨)

**방식**: JSON body의 `metadata.apiKey` 필드

```json
{
  "metadata": {
    "apiKey": "ya29.a0AXooCgt..."
  },
  "request": {
    ...
  }
}
```

> **참고**: ZeroGravity README에서 명시적으로 "The token is in the JSON body under `metadata.apiKey`, not in an HTTP header"라고 언급됩니다.

### 5.2 현재 AntigravityExecutor (buildRequest)

**방식**: HTTP Authorization 헤더

```go
httpReq.Header.Set("Authorization", "Bearer "+token)
```

**파일**: `CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go:1380`

### 5.3 차이점 매핑

| 항목 | LS Binary API | 현재 Executor |
|------|---------------|---------------|
| **토큰 위치** | `metadata.apiKey` (JSON body) | `Authorization` 헤더 |
| **토큰 형식** | `ya29.xxx` (OAuth) | `ya29.xxx` (OAuth) |
| **프로토콜** | JSON-RPC over HTTP | HTTP REST |
| **엔드포인트** | `/` (JSON-RPC) | `/v1/chat/completions` 등 |
| **Content-Type** | `application/json` | `application/json` |

## 6. 모델 이름 매핑

ZeroGravity에서 사용하는 모델 이름:

| 모델명 | 실제 모델 |
|--------|-----------|
| `opus-4.6` | Claude Opus 4.6 (Thinking) |
| `sonnet-4.6` | Claude Sonnet 4.6 (Thinking) |
| `opus-4.5` | Claude Opus 4.5 (Thinking) |
| `gemini-3.1-pro` | Gemini 3.1 Pro (High) |
| `gemini-3.1-pro-high` | Gemini 3.1 Pro (High) |
| `gemini-3.1-pro-low` | Gemini 3.1 Pro (Low) |
| `gemini-3-pro` | Gemini 3 Pro (High) |
| `gemini-3-pro-high` | Gemini 3 Pro (High) |
| `gemini-3-pro-low` | Gemini 3 Pro (Low) |
| `gemini-3-flash` | Gemini 3 Flash |

## 7. 요청/응답 변환 필요 사항

### 7.1 OpenAI → LS Binary

현재 `geminiToAntigravity()` 함수가 OpenAI 형식을 Antigravity 형식으로 변환합니다.

**필요한 수정**:
1. 토큰을 JSON body의 `metadata.apiKey`로 이동
2. JSON-RPC 래퍼 추가 (`jsonrpc`, `method`, `params`, `id`)
3. 엔드포인트를 LS 바이너리의 HTTP 포트로 변경

### 7.2 LS Binary → OpenAI

스트리밍 및 비스트리밍 응답을 OpenAI 형식으로 변환해야 합니다.

## 8. 실행 시 로그 출력

```
I0220 17:05:49.960293 1335325 server.go:1201] Starting language server process with pid 1335325
I0220 17:05:49.960665 1335325 server.go:289] Setting GOMAXPROCS to 4
I0220 17:05:49.963897 1335325 server.go:481] Language server will attempt to listen on host 127.0.0.1
I0220 17:05:50.000134 1335325 server.go:496] Language server listening on fixed port at 42100 for HTTPS
I0220 17:05:50.010229 1335325 server.go:503] Language server listening on fixed port at 39251 for HTTP
I0220 17:05:50.306618 1335325 log_context.go:115] Please visit the following URL to authorize the application:
https://accounts.google.com/o/oauth2/auth?...
I0220 17:05:50.313192 1335325 launchmanager.go:78] Entering local chrome mode! This is WRONG unless you are running tests or in eval mode on Linux.
I0220 17:05:50.320674 1335325 server.go:1579] initialized server successfully in 360.303901ms
```

## 9. 구현 참고사항

### 9.1 Subprocess 실행

```go
cmd := exec.CommandContext(ctx, lsBinaryPath,
    "-standalone=true",
    "-api_server_url=http://127.0.0.1:"+port,
    "-gemini_dir="+dataDir+"/gemini",
    "-app_data_dir="+dataDir+"/data",
)
```

### 9.2 Health Check

LS 바이너리는 기본적인 HTTP 서버를 제공하지만, 표준 `/health` 엔드포인트는 없습니다. 대신 TCP 연결 확인이나 초기 요청 성공 여부로 판단해야 합니다.

### 9.3 프로세스 관리

- LS 바이너리는 OAuth 인증이 필요합니다 (첫 실행 시)
- `state.vscdb` 파일을 통해 refresh token을 관리합니다
- 프로세스 종료 시 적절한 cleanup 필요

## 10. 관련 파일

- `CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go` - 현재 Executor 구현
- `/home/jc01rho/git/zerogravity/README.md` - ZeroGravity 문서
- `/home/jc01rho/git/zerogravity/Dockerfile` - LS 바이너리 다운로드/설정
- `/home/jc01rho/git/zerogravity/scripts/setup-linux.sh` - LS 바이너리 탐지/설치

## 11. 테스트 후 정리

```bash
# LS 바이너리 프로세스 종료
killall language_server_linux_x64

# 임시 파일 정리
rm -rf /tmp/ls-test
```

---

**다음 단계**: 이 사양을 바탕으로 `AntigravityExecutor`를 LS 바이너리 서브프로세스 방식으로 마이그레이션합니다.

---

## 12. ZeroGravity 모델 목록 구현 방식 분석 (2026-02-20)

### 12.1 결론 요약

ZeroGravity 소스는 비공개이지만, README + Dockerfile + CLIProxyAPIPlus 구현으로 전체 방식을 파악.

**ZeroGravity의 `/v1/models` 흐름**:
1. LS 바이너리(`language_server_linux_x64`)를 standalone 모드로 실행
2. JSON-RPC `FetchAvailableModels` 메서드 호출 (CLIProxyAPIPlus 구현 기준)
   - README에선 `GetCommandModelConfigs`라고 언급하지만, 이는 Antigravity IDE → LS 내부 메서드명으로 추정
3. LS 응답에서 `result.models` 맵 파싱
4. 특정 내부 모델명 필터링 (`chat_20706`, `chat_23310`, `gemini-2.5-flash-thinking`, `gemini-3-pro-low`, `gemini-2.5-pro`)
5. 각 모델에 정적 설정 보강(Thinking 지원 범위, MaxCompletionTokens)

### 12.2 실제 LS 응답 모델명 vs ZeroGravity 공개 모델명

| ZeroGravity README 모델명 | LS 응답 실제 모델명 (추정) |
|--------------------------|--------------------------|
| `opus-4.6` | `claude-opus-4-6-thinking` |
| `sonnet-4.6` | `claude-sonnet-4-6-thinking` 또는 `claude-sonnet-4-6` |
| `opus-4.5` | `claude-opus-4-5-thinking` |
| `gemini-3-pro` | `gemini-3-pro-high` (기본) |
| `gemini-3-pro-high` | `gemini-3-pro-high` (alias) |
| `gemini-3-pro-low` | `gemini-3-pro-low` (필터링됨!) |
| `gemini-3-flash` | `gemini-3-flash` |
| `gemini-3.1-pro` | 미확인 (아직 LS에서 미지원 가능) |

> **주의**: `gemini-3-pro-low`는 CLIProxyAPIPlus의 `FetchAntigravityModels()`에서 필터링되지만 ZeroGravity README에는 노출됨 → ZeroGravity는 별도 alias 처리 가능성

### 12.3 CLIProxyAPIPlus 현재 구현과의 비교

| 항목 | ZeroGravity (추정) | CLIProxyAPIPlus |
|------|-------------------|----------------|
| 모델 소스 | LS `FetchAvailableModels` | 동일 (`FetchAntigravityModels`) |
| 정적 보강 | 내부 config | `GetAntigravityModelConfig()` |
| 모델명 노출 | 짧은 alias (`opus-4.6`) | LS 원본명 (`claude-opus-4-6-thinking`) |
| 필터링 | 내부 모델 제거 | 동일 (chat_*, gemini-2.5-flash-thinking 등) |

### 12.4 CLIProxyAPIPlus에 적용할 수 있는 개선사항

1. **ZeroGravity 스타일 alias**: LS 원본 모델명에서 친숙한 짧은 이름으로 alias 제공 가능
   - `claude-opus-4-6-thinking` → `opus-4.6`
   - `claude-sonnet-4-6-thinking` → `sonnet-4.6`
   - `gemini-3-flash` → `gemini-3-flash` (그대로)

2. **모델 목록은 이미 완성됨**: `FetchAntigravityModels()` (line 907)가 이미 동적 LS 호출 구현

3. **정적 fallback**: `GetStaticModelsForProvider("antigravity")`는 LS 응답 실패 시 static config에서 기본 목록 반환

