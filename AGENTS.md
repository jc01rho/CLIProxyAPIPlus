# PROJECT KNOWLEDGE BASE

**Generated:** 2026-02-09
**Type:** Monorepo (Backend + Frontend + Dashboard)

## OVERVIEW

CLI Proxy API: AI 모델 프록시 시스템. Go 백엔드가 다중 AI 프로바이더(Claude, Gemini, OpenAI, Codex, Kiro 등)를 OpenAI 호환 API로 통합. React 프론트엔드가 관리 대시보드 제공.

## REMOVED PROVIDERS

### Trae Provider (Removed 2026-02-09)
The Trae provider has been completely removed from the codebase. All Trae-related authentication, translation, and configuration code has been deleted from the backend. The provider is no longer supported or available in the API.

## STRUCTURE

```
cli-proxy/
├── CLIProxyAPIPlus/                  # Go 백엔드 (API 프록시 서버)
│   ├── cmd/server/                   # CLI 진입점
│   ├── internal/                     # 핵심 구현 (비공개)
│   │   ├── api/                      # HTTP 서버, 라우팅
│   │   ├── auth/                     # 프로바이더별 OAuth
│   │   ├── translator/               # 프로토콜 변환 엔진
│   │   └── runtime/executor/         # 요청 실행기
│   ├── sdk/                          # Public SDK
│   └── AGENTS.md                     # 백엔드 상세 문서
│
├── Cli-Proxy-API-Management-Center/  # React 프론트엔드 (관리 UI)
│   ├── src/
│   │   ├── pages/                    # 라우트별 페이지
│   │   ├── components/               # UI 컴포넌트
│   │   ├── services/api/             # 백엔드 API 클라이언트
│   │   ├── stores/                   # Zustand 상태 관리
│   │   └── types/                    # TypeScript 타입
│   └── AGENTS.md                     # 프론트엔드 상세 문서
│
└── CLIProxyAPI-Dashboard/            # 모니터링 대시보드
    ├── collector/                    # Python 수집기 (Flask + APScheduler)
    ├── frontend/                     # React 대시보드 (Vite)
    └── AGENTS.md                     # 대시보드 상세 문서
```

## BACKEND-FRONTEND INTEGRATION

### API 연결

| Frontend | Backend Endpoint | Purpose |
|----------|------------------|---------|
| `services/api/providers.ts` | `/v0/management/auths/*` | 프로바이더 인증 관리 |
| `services/api/apiKeys.ts` | `/v0/management/api-keys/*` | API 키 CRUD |
| `services/api/config.ts` | `/v0/management/config/*` | 서버 설정 |
| `services/api/logs.ts` | `/v0/management/logs/*` | 로그 조회 |
| `services/api/usage.ts` | `/v0/management/usage/*` | 사용량 통계 |
| `services/api/oauth.ts` | `/v0/management/oauth/*` | OAuth 콜백 |

### 인증 흐름

```
Frontend (React)                    Backend (Go)
     │                                   │
     │ ── Management Key ──────────────> │ middleware/auth.go
     │ <── Bearer Token ─────────────────│
     │                                   │
     │ ── /v0/management/* ────────────> │ handlers/management/
     │                                   │
```

### 공유 타입 매핑

| Frontend Type | Backend Type | Location |
|---------------|--------------|----------|
| `Auth` (types/auth.ts) | `auth.Auth` | sdk/cliproxy/auth/ |
| `Config` (types/config.ts) | `config.Config` | internal/config/ |
| `UsageStats` (types/usage.ts) | `usage.Event` | sdk/cliproxy/usage/ |

## WHERE TO LOOK

| Task | Backend | Frontend |
|------|---------|----------|
| 새 AI 프로바이더 추가 | `internal/auth/{provider}/` | `components/providers/{Provider}Section/` |
| API 엔드포인트 추가 | `internal/api/handlers/management/` | `services/api/` |
| 프로토콜 변환 | `internal/translator/{src}/{tgt}/` | N/A |
| UI 컴포넌트 추가 | N/A | `components/` |
| 상태 관리 | N/A | `stores/` |
| 설정 필드 추가 | `internal/config/config.go` | `types/config.ts` |

## TECH STACK

| Layer | Technology |
|-------|------------|
| **Backend** | Go 1.24, Gin Framework, logrus |
| **Frontend** | React 19, TypeScript, Vite, Zustand, axios |
| **i18n** | react-i18next (en, zh-CN) |
| **Charts** | chart.js + react-chartjs-2 |
| **Styling** | SCSS modules |

## COMMANDS

```bash
# Backend
cd CLIProxyAPIPlus
go build -o cliproxy ./cmd/server
./cliproxy -c config.yaml
go test ./...

# Frontend
cd Cli-Proxy-API-Management-Center
npm install
npm run dev      # 개발 서버 (Vite)
npm run build    # 프로덕션 빌드
npm run lint     # ESLint 검사
```

## ANTI-PATTERNS (GLOBAL)

### Backend
- **NEVER** `http.DefaultClient` 사용 → `util.NewProxyClient()` 사용
- **NEVER** 토큰 직접 로깅 → 마스킹 필수
- **NEVER** translator에서 HTTP 호출 → 변환만 담당

### Frontend
- **NEVER** API 직접 호출 → `services/api/` 통해 호출
- **NEVER** 전역 상태 직접 수정 → Zustand store 사용
- **NEVER** `as any` 타입 단언 → 적절한 타입 정의

## CONVENTIONS

### 파일 명명
- Backend: `{provider}_auth.go`, `{src}_{tgt}_request.go`
- Frontend: `{Name}Page.tsx`, `use{Name}.ts`, `{name}.ts` (서비스)

### 코드 스타일
- Backend: Go 표준, logrus 로깅
- Frontend: ESLint + Prettier, 함수형 컴포넌트 + hooks

## SUB-DOCUMENTS

| Path | Scope |
|------|-------|
| [CLIProxyAPIPlus/AGENTS.md](./CLIProxyAPIPlus/AGENTS.md) | 백엔드 전체 |
| [CLIProxyAPIPlus/internal/AGENTS.md](./CLIProxyAPIPlus/internal/AGENTS.md) | internal 패키지 |
| [CLIProxyAPIPlus/sdk/AGENTS.md](./CLIProxyAPIPlus/sdk/AGENTS.md) | SDK 패키지 |
| [Cli-Proxy-API-Management-Center/AGENTS.md](./Cli-Proxy-API-Management-Center/AGENTS.md) | 프론트엔드 전체 |
| [CLIProxyAPI-Dashboard/AGENTS.md](./CLIProxyAPI-Dashboard/AGENTS.md) | 대시보드 전체 |
| [CLIProxyAPI-Dashboard/collector/AGENTS.md](./CLIProxyAPI-Dashboard/collector/AGENTS.md) | Python 수집기 |
