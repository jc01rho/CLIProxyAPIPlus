# Trae AI Research - Key Learnings

## Date: 2026-01-28

### What is Trae AI?

Trae AI is ByteDance's AI-powered development platform with two distinct products:

1. **Trae IDE** - Desktop application (VSCode-based) with AI coding assistance
2. **Trae Agent** - Open-source CLI tool for software engineering tasks

### Authentication Architecture

**Trae IDE uses OAuth 2.0 authentication:**
- Requires: APP_ID, CLIENT_ID, REFRESH_TOKEN, USER_ID, AUTH_TOKEN
- Flow: OAuth login → Refresh token → Access tokens → API requests
- Region-specific endpoints (Singapore region observed)

**Trae Agent uses provider-specific API keys:**
- Each LLM provider (OpenAI, Anthropic, etc.) requires separate API key
- Configuration via YAML files or environment variables
- No unified authentication - delegates to underlying LLM providers

### API Endpoints Discovered

**Base URLs:**
- Primary API: `https://trae-api-sg.mchost.guru`
- Auth/Refresh: `https://api-sg-central.trae.ai`
- File services: ByteDance CDN endpoints

**Key Endpoints:**
- `GET /api/ide/v1/model_list` - List available models
- `POST /api/ide/v1/chat` - Chat completions (streaming supported)
- `GET /api/ide/v1/text_to_image` - Image generation

**Required Headers:**
- `x-app-id`: Application identifier
- `x-device-brand`: Device information
- `x-device-cpu`: CPU information
- `Authorization: Bearer <token>`: Access token

### Code Examples Found

**Community Projects:**
1. **trae2api** (Go) - OpenAI-compatible wrapper (archived)
2. **trae-api** (Python) - Direct API integration examples
3. **trae-agent** (Python) - Official CLI tool (10.7k+ stars)

### Important Patterns

1. **Dual Authentication Models**: IDE uses OAuth, Agent uses provider keys
2. **Streaming Support**: API supports SSE for real-time responses
3. **OpenAI Compatibility**: Community wrappers convert to OpenAI format
4. **Free Tier**: Currently offers free access to premium models
5. **Rate Limiting**: Subject to queuing and rate limits

### Technical Insights

- Built on VSCode foundation with custom UI layer
- Supports multiple LLM providers (Claude, GPT-4, Gemini, etc.)
- MIT-licensed agent component encourages research/extension
- Active community with wrapper projects and integrations
- Region-specific deployment (Singapore endpoints visible)

### Limitations Discovered

- Official API documentation is sparse
- Authentication credentials must be extracted from IDE app
- Community wrapper (trae2api) is archived/unmaintained
- Version compatibility issues (v1.3.0+ has breaking changes)
- No official public API for third-party integration

### Research Quality

- Found official GitHub repository with 10.7k stars
- Located community integration examples
- Identified authentication flow from reverse-engineered wrappers
- Discovered API endpoints from open-source projects
- Technical report available: arxiv.org/abs/2507.23370
## Trae IDE 설치 경로 및 API 엔드포인트 탐색 결과

### 탐색 결과 요약
- **Trae IDE 설치 상태**: 로컬 시스템에 설치되지 않음
- **발견된 관련 파일**: CLI Proxy 프로젝트 내 Trae 통합 코드
- **API 엔드포인트 정보**: 기존 연구 및 구현에서 확인

### 발견된 API 엔드포인트 정보

#### 1. 주요 API 베이스 URL
- **Primary API**: `https://trae-api-sg.mchost.guru`
- **Auth/OAuth**: `https://api-sg-central.trae.ai`
- **Authorization Page**: `https://www.trae.ai/authorization`

#### 2. 핵심 엔드포인트
- **Chat Completions**: `POST /api/ide/v1/chat`
- **Model List**: `GET /api/ide/v1/model_list`
- **Image Generation**: `GET /api/ide/v1/text_to_image`

#### 3. 인증 정보
- **APP_ID**: `trae_ide` (기본값)
- **CLIENT_ID**: `ono9krqynydwx5` (OAuth에서 발견)
- **토큰 형식**: JWT (userJwt 파라미터)

#### 4. 필수 헤더
```
x-app-id: trae_ide
x-ide-version: 1.2.10
x-ide-version-code: 20250325
x-ide-version-type: stable
x-device-cpu: AMD
x-device-id: [동적 생성]
x-machine-id: [동적 생성]
x-device-brand: [동적 생성]
x-device-type: windows
x-ide-token: [access_token]
accept: */*
Connection: keep-alive
```

### 5. OAuth 플로우
1. **Authorization URL 생성**: 
   - Base: `https://www.trae.ai/authorization`
   - 파라미터: login_version, auth_from, login_channel, plugin_version, auth_type, client_id, redirect, login_trace_id, auth_callback_url, machine_id, device_id, x_device_*
2. **콜백 처리**: `/authorize` 엔드포인트에서 userJwt, userInfo 파싱
3. **토큰 저장**: JWT 직접 저장 (토큰 교환 불필요)

### 6. 요청/응답 형식

#### 요청 형식 (Trae 전용)
```json
{
  "user_input": "Hello",
  "intent_name": "general_qa_intent",
  "variables": "{...}",
  "context_resolvers": [...],
  "chat_history": [...],
  "session_id": "[hash]",
  "model_name": "claude3.5"
}
```

#### 응답 형식 (SSE)
```
event: output
data: {"response": "Hello", "reasoning_content": "", "finish_reason": ""}

event: done
data: {"finish_reason": "stop"}
```

### 7. 모델명 매핑
- `claude-3-5-sonnet` → `claude3.5`
- `claude-3-7-sonnet` → `aws_sdk_claude37_sonnet`
- `gpt-4o-mini` → `gpt-4o`
- `deepseek-chat` → `deepseek-V3`
- `deepseek-reasoner` → `deepseek-R1`

### 8. 구현 상태
- ✅ **Native OAuth**: 완전 구현됨
- ✅ **Executor**: Execute/ExecuteStream 구현 완료
- ✅ **Protocol Translation**: OpenAI ↔ Trae 변환 구현
- ✅ **Frontend Integration**: TraeSection 컴포넌트 구현

### 9. 테스트 방법
```bash
# 1. OAuth URL 생성 테스트
curl -s "http://localhost:8080/v0/management/trae-auth-url?is_webui=true" \
  -H "Authorization: Bearer ${MGMT_KEY}" | jq .

# 2. Chat Completion 테스트
curl -X POST "http://localhost:8080/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -d '{"model": "claude-3-5-sonnet", "messages": [{"role": "user", "content": "Hello"}]}'
```

### 10. 주요 파일 위치
- **Backend Executor**: `CLIProxyAPIPlus/internal/runtime/executor/trae_executor.go`
- **OAuth Implementation**: `CLIProxyAPIPlus/internal/auth/trae/`
- **Frontend Component**: `Cli-Proxy-API-Management-Center/src/components/providers/TraeSection/`
- **Test Credentials**: `CLIProxyAPIPlus/.cli-proxy-api/trae-*.json`

### 결론
Trae IDE는 로컬에 설치되지 않았지만, CLI Proxy 프로젝트에서 이미 완전한 Trae API 통합을 구현했습니다. 모든 필요한 API 엔드포인트, 인증 플로우, 프로토콜 변환이 구현되어 있어 즉시 사용 가능한 상태입니다.
