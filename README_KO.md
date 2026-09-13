<div align="center">

# CLI Proxy API Plus

**모든 AI 구독을 하나의 로컬 엔드포인트로 — [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)의 프로덕션 강화 fork, [jc01rho](https://github.com/jc01rho)가 유지 관리**

[![Tag](https://img.shields.io/github/v/tag/jc01rho/CLIProxyAPIPlus?label=release)](https://github.com/jc01rho/CLIProxyAPIPlus/tags)
[![Go Version](https://img.shields.io/github/go-mod/go-version/jc01rho/CLIProxyAPIPlus)](go.mod)
[![License](https://img.shields.io/github/license/jc01rho/CLIProxyAPIPlus)](LICENSE)
[![Upstream](https://img.shields.io/badge/upstream-router--for--me%2FCLIProxyAPI-blue)](https://github.com/router-for-me/CLIProxyAPI)

[English](README.md) | [中文](README_CN.md) | [日本語](README_JA.md) | 한국어

</div>

## 이 fork를 선택하는 이유

이 fork는 upstream을 충실히 따라가면서, 프로덕션 환경에서 멀티 계정 풀을 운영하며 얻은 수정 사항을 추가합니다:

| 영역 | 이 fork의 강화점 |
|---|---|
| **폴백 및 쿨다운** | HTTP 400 응답도 모델 폴백 체인을 트리거(upstream은 401/403/429/5xx만 폴백); 429 레이트 리밋 쿨다운을 최대 24시간까지 연장 |
| **프로바이더별 수정** | DeepSeek 계열 프로바이더(DeepSeek.com, nano-gpt.com, deepseek 접두사 모델, nanogpt 호환)의 `interleaved` 콘텐츠 블록 제거; Mistral의 빈 assistant 메시지 필터링; xAI reasoning 입력에서 `encrypted_content` 제거; xAI의 200개 도구 상한 강제; Xiaomi 프로바이더 이름 접두사 매칭 및 reasoning replay 백필 |
| **관리 API** | 누락된 `/v0/management/request-log-success-body` 라우트 복원 |
| **에이전트 네이티브** | 에이전트 기반 워크플로우를 위한 포괄적인 `AGENTS.md` 지식 베이스 |

데스크톱에서 CLIProxyAPI를 사용하고 싶다면 [EasyCLIProxyAPI](https://github.com/router-for-me/EasyCLIProxyAPI) 데스크톱 클라이언트를 추천합니다. 그래픽 설정 UI, 자동 업데이트, 시스템 트레이 통합, CLIProxyAPI 서비스 원클릭 시작/중지 기능을 제공합니다.

CLIProxyAPI는 CLI 도구를 위한 OpenAI/Gemini/Claude/Codex/Grok 호환 API 인터페이스를 제공하는 프록시 서버입니다.

로컬 환경이나 여러 CLI 계정을 통해 OpenAI(Responses 포함), Gemini(Interactions 포함), Claude 호환 클라이언트 또는 SDK로 다음 프로바이더에 접근할 수 있습니다.

<table>
<tbody>
    <tr>
        <th align="center" width="100">프로바이더</th>
        <th align="center">설명</th>
    </tr>
    <tr>
        <td align="center"><a href="https://www.kimi.com/code/?aff=cliproxyapi"><img src="./assets/logo/kimi.svg" alt="Kimi" width="28" height="28" /></a></td>
        <td>Kimi 시리즈 모델(Kimi K3, Kimi K2.7 Code 등). <a href="https://platform.kimi.ai/docs/guide/kimi-k3-quickstart">Kimi K3</a>는 Moonshot AI의 가장 강력한 모델이자 세계 최초의 오픈 3T급 모델입니다. 2.8조 파라미터, 네이티브 비전, 100만 토큰 컨텍스트로 장기 코딩, 지식 작업, 추론에 적합합니다. CLIProxyAPI는 OAuth 또는 호환 API로 Kimi를 지원합니다. <a href="https://www.kimi.com/code/?aff=cliproxyapi">Kimi Code 구독</a>을 사용해 보거나 <a href="https://platform.kimi.com">Kimi 플랫폼</a>에서 API 키를 발급받으세요.</td>
    </tr>
    <tr>
        <td align="center"><a href="https://openai.com"><img src="./assets/logo/openai.svg" alt="OpenAI" width="28" height="28" /></a></td>
        <td>OpenAI GPT 시리즈 모델(GPT 5.6, GPT 5.5 등). GPT-5.6은 복잡한 프로덕션 워크플로우의 새로운 품질·효율 기준을 세웠습니다. 특히 토큰을 절약하고 레이아웃, 시각적 계층, 디자인 판단력을 포함한 프론트엔드 미적 표현을 개선했습니다.</td>
    </tr>
    <tr>
        <td align="center"><a href="https://www.anthropic.com/claude"><img src="./assets/logo/claude.svg" alt="Anthropic" width="28" height="28" /></a></td>
        <td>Anthropic Claude 시리즈 모델(Claude Fable, Claude Opus, Claude Sonnet 등). Claude Fable 5는 Anthropic 공개 모델 중 가장 강력하며, 가장 까다로운 추론 및 장기 에이전트 작업을 위해 설계되었습니다.</td>
    </tr>
    <tr>
        <td align="center"><a href="https://antigravity.google/"><img src="./assets/logo/antigravity.svg" alt="Antigravity" width="28" height="28" /></a></td>
        <td>Google Gemini 시리즈 모델(Gemini 3.5 Flash, Gemini 3.1 Pro 등). Gemini 3.5 Flash는 실제 작업에 최적화된 지속적인 프론티어급 지능을 더 빠른 속도와 낮은 비용으로 제공합니다. 에이전트 시대를 위해 설계되어 서브 에이전트 배포, 다단계 워크플로우, 대규모 장기 작업에 강합니다. 복잡한 코딩 루프와 반복이 포함된 빠른 에이전트 루프에 특히 적합합니다.</td>
    </tr>
    <tr>
        <td align="center"><a href="https://x.ai/grok"><img src="./assets/logo/xai.svg" alt="xAI" width="28" height="28" /></a></td>
        <td>xAI Grok 시리즈 모델(Grok 4.5, Grok Composer 2.5 Fast 등). Grok 4.5는 SpaceXAI가 코딩, 에이전트 작업, 지식 작업을 위해 만든 프론티어 모델입니다. 멤피스 데이터센터에서 과학, 공학, 수학을 아우르는 새 데이터셋으로 학습되었습니다.</td>
    </tr>
    <tr>
        <td align="center"><a href="https://dev.meta.ai">Meta</a></td>
        <td>Meta Muse Spark 시리즈 모델(Muse Spark 1.3 등). Muse Spark는 Meta의 코딩 특화 모델로, 1M 토큰 컨텍스트 윈도우, 네이티브 멀티모달 입력(텍스트, 이미지, PDF, 비디오), 턴 간 이어지는 추론을 지원합니다. CLIProxyAPI는 <code>https://api.meta.ai/v1</code>의 OpenAI 호환 Model API를 통해 Meta를 지원합니다.</td>
    </tr>
</tbody>
</table>

## 기능

- CLI 모델용 OpenAI/Gemini/Claude/Codex/Grok 호환 API 엔드포인트
- OpenAI Codex(GPT 시리즈) 지원(OAuth 로그인)
- Claude Code 지원(OAuth 로그인)
- Grok Build 지원(OAuth 로그인)
- 스트리밍, 비스트리밍 응답 및 지원되는 경우 WebSocket 응답
- 함수 호출/도구 지원
- 멀티모달 입력(텍스트, 이미지)
- 다중 계정 및 라운드 로빈 로드 밸런싱(Gemini, OpenAI, Claude, Grok)
- 간단한 CLI 인증 플로우(Gemini, OpenAI, Claude, Grok)
- Gemini AIStudio API 키 지원
- AI Studio Build 다중 계정 로드 밸런싱
- Claude Code 다중 계정 로드 밸런싱
- OpenAI Codex 다중 계정 로드 밸런싱
- Grok Build 다중 계정 로드 밸런싱
- 네이티브 Freebuff(Codebuff) executor: 세션 재사용 및 `cost_mode: free`
- 설정으로 OpenAI 호환 업스트림 프로바이더 연결(예: OpenRouter)
- 재사용 가능한 Go SDK(`docs/sdk-usage.md` 참조)

## 시작하기

CLIProxyAPI 가이드: [https://help.router-for.me/](https://help.router-for.me/)

## 관리 API 문서

[MANAGEMENT_API.md](https://help.router-for.me/management/api)를 참조하세요.

## 사용량 통계

v6.10.0 이후 CLIProxyAPI와 [CPAMC](https://github.com/router-for-me/Cli-Proxy-API-Management-Center)는 내장 사용량 통계를 제공하지 않습니다. 사용량 통계가 필요하면 다음 프로젝트를 사용하세요:

- [Keeper 사용량 export 운영 런북](docs/keeper-export.md)

### [CPA Usage Keeper](https://github.com/jc01rho/cpa-usage-keeper) (권장)

> **사용량 추적은 이것으로.** 이 fork의 CLIProxyAPIPlus 전용 대시보드: 주기적 데이터 동기화, SQLite 저장, 집계 API, 사용량·통계 내장 대시보드를 갖춘 독립형 지속성·시각화 서비스입니다. 하나의 Keeper로 여러 CPA 인스턴스를 관리할 수 있고, bearer 자격 증명 push 프로토콜(`/api/v1/export/*`, [Keeper export 런북](docs/keeper-export.md) 참조)도 지원합니다.

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/jc01rho/cpa-usage-keeper/main/assets/screenshots/overview-dark.png" />
    <source media="(prefers-color-scheme: light)" srcset="https://raw.githubusercontent.com/jc01rho/cpa-usage-keeper/main/assets/screenshots/overview-light.png" />
    <img src="https://raw.githubusercontent.com/jc01rho/cpa-usage-keeper/main/assets/screenshots/overview-light.png" alt="CPA Usage Keeper 개요 대시보드" width="720" />
  </picture>
</p>

### [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus)

요청 수준 모니터링과 비용 추정을 제공하는 완전한 CLIProxyAPI 관리 센터입니다. CPA-Manager는 수집된 요청을 계정, 모델, 채널, 지연 시간, 상태, 토큰 사용량별로 추적하고, 편집 가능한 모델 가격과 원클릭 LiteLLM 가격 동기화로 비용을 추정하며, SQLite에 이벤트를 저장하고, Codex 계정 풀 운영(일괄 점검, 쿼터 감지, 비정상 계정 발견, 정리 제안, 원클릭 실행)을 제공합니다.

## SDK 문서

- 사용법: [docs/sdk-usage.md](docs/sdk-usage.md)
- 고급(executor 및 translator): [docs/sdk-advanced.md](docs/sdk-advanced.md)
- 인증: [docs/sdk-access.md](docs/sdk-access.md)
- 자격 증명 로드/갱신: [docs/sdk-watcher.md](docs/sdk-watcher.md)
- 커스텀 프로바이더 예제: `examples/custom-provider`

## 통합

- **GitLab Duo 프로바이더**: [docs/gitlab-duo.md](docs/gitlab-duo.md) — GitLab Duo를 일급 프로바이더로 사용

## 기여

기여를 환영합니다! Pull Request를 자유롭게 제출해 주세요.

1. 저장소를 Fork합니다
2. 기능 브랜치를 만듭니다 (`git checkout -b feature/amazing-feature`)
3. 변경 사항을 커밋합니다 (`git commit -m 'Add some amazing feature'`)
4. 브랜치에 푸시합니다 (`git push origin feature/amazing-feature`)
5. Pull Request를 엽니다

## 함께하는 프로젝트

다음 프로젝트들은 CLIProxyAPI를 기반으로 합니다:

### [vibeproxy](https://github.com/automazeio/vibeproxy)

Claude Code & ChatGPT 구독을 AI 코딩 도구와 함께 사용할 수 있는 네이티브 macOS 메뉴 바 앱 — API 키 불필요.

### [Subtitle Translator](https://github.com/VjayC/SRT-Subtitle-Translator-Validator)

CLIProxyAPI를 통해 기존 LLM 구독(Gemini, ChatGPT, Claude 등)으로 SRT 자막을 번역·검증하는 크로스 플랫폼 데스크톱·웹 앱 — API 키 불필요.

### [CCS (Claude Code Switch)](https://github.com/kaitranntt/ccs)

CLIProxyAPI OAuth로 여러 Claude 계정과 대체 모델(Gemini, Codex, Antigravity)을 즉시 전환하는 CLI 래퍼 — API 키 불필요.

### [Quotio](https://github.com/nguyenphutrong/quotio)

Claude, Gemini, OpenAI, Antigravity 구독을 통합 관리하는 네이티브 macOS 메뉴 바 앱. Claude Code, OpenCode, Droid 같은 AI 코딩 도구를 위한 실시간 쿼터 추적과 스마트 자동 폴백 제공 — API 키 불필요.

### [ProxyPilot](https://github.com/Finesssee/ProxyPilot)

TUI, 시스템 트레이, 멀티 프로바이더 OAuth를 갖춘 Windows 네이티브 CLIProxyAPI fork — API 키 불필요.

### [Claude Proxy VSCode](https://github.com/uzhao/claude-proxy-vscode)

VSCode에서 Claude 구독을 사용할 수 있게 해주는 확장.

### [ZeroLimit](https://github.com/0xtbug/zero-limit)

Tauri + React 기반 Windows 데스크톱 앱으로, CLIProxyAPI를 통해 AI 코딩 어시스턴트 쿼터를 모니터링합니다. Gemini, Claude, OpenAI Codex, Antigravity 계정의 사용량 추적, 실시간 대시보드, 시스템 트레이 통합, 원클릭 프록시 제어 지원 — API 키 불필요.

### [CPA-XXX Panel](https://github.com/ferretgeek/CPA-X)

CLIProxyAPI용 웹 관리 패널. 헬스 체크, 리소스 모니터링, 로그 조회, 자동 업데이트, 요청 통계, 가격 표시를 제공하며 원클릭 설치와 systemd 서비스를 지원합니다.

### [CLIProxyAPI Tray](https://github.com/kitephp/CLIProxyAPI_Tray)

PowerShell 스크립트 기반 Windows 트레이 앱. 서드파티 라이브러리 없이 자동 시작, 프록시 제어 등을 제공합니다.

### [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard)

Next.js, React, PostgreSQL로 만든 현대적인 웹 기반 CLIProxyAPI 관리 대시보드. 실시간 로그 스트리밍, 구조화된 설정 편집, API 키 관리, Claude/Gemini/Codex OAuth 프로바이더 통합, 사용량 분석, 컨테이너 관리, 컴패니언 플러그인으로 OpenCode와 설정 동기화 — 수동 YAML 편집 불필요.

### [All API Hub](https://github.com/qixing-jk/all-api-hub)

New API 호환 중계 계정을 한곳에서 관리하는 브라우저 확장. 잔액·사용량 대시보드, 자동 체크인, 자주 쓰는 앱으로 키 원클릭보내기, 웹 내 API 가용성 테스트, 채널·모델 동기화·리다이렉션 제공. CLIProxyAPI Management API로 원클릭 임포트 지원.

### [CLIProxyAPI Quota Inspector](https://github.com/AllenReder/CLIProxyAPI-Quota-Inspector)

바로 사용 가능한 CLIProxyAPI 크로스 플랫폼 쿼터 조회 도구. 계정별 codex 5h/7d 쿼터 윈도우, 플랜별 정렬, 상태 색상, 다중 계정 요약 분석 지원.

### [CLIProxy Pool Watch](https://github.com/murasame612/CLIProxyPoolWidget)

CLIProxyAPI 풀의 ChatGPT/Codex 계정 쿼터를 모니터링하는 네이티브 macOS SwiftUI 앱. Management API로 계정 가용 상태를 표시합니다.

### [Quotio Desktop](https://github.com/xiaocoss/quotio-desktop)

Quotio의 크로스 플랫폼(Tauri) 포트로 Windows, macOS, Linux 지원. CLIProxyAPI로 다중 AI 계정 풀(Codex, Claude Code, GitHub Copilot, Gemini CLI, Antigravity, Kiro, Cursor, Trae, GLM)을 관리하며, 계정별 5시간/주간 쿼터 바, Codex 레이트 리밋 리셋 크레딧과 원클릭 리셋, 스마트 스케줄링, 사용량 통계, Codex 멀티 인스턴스 제공 — API 키 불필요.

### [Universal Chat Provider](https://github.com/maxdewald/vscode-universal-chat-provider)

Claude, ChatGPT/Codex, Antigravity, Grok, Kimi 구독을 GitHub Copilot Chat의 네이티브 언어 모델로 가져오는 VS Code 확장 — Git 커밋 메시지, 채팅 제목 등에도 활용 가능.

### [WebBrain](https://github.com/webbrain-one/webbrain)

CLIProxyAPI의 로컬 OpenAI 호환 엔드포인트를 모델 프로바이더로 연결할 수 있는 브라우저 에이전트. EasyCLIProxyAPI를 통한 사용법은 WebBrain의 독립 [설정·보안·계정 위험 가이드](https://webbrain.one/docs/easy-cli-proxy/)를 참조하세요.

### [Infinitus](https://github.com/deathemperor/infinitus)

CLIProxyAPI의 Management API로 Claude 계정 플릿을 운영하는 네이티브 macOS 메뉴 바 앱(claude-swap과 9Router도 지원): 5시간/7일/모델별 쿼터 게이지, 팝업에서 전환/보류/즐겨찾기, 각 윈도우 소진 시점 예측, iPhone 컴패니언 미러링 — API 키 불필요.

> [!NOTE]
> CLIProxyAPI 기반 프로젝트를 개발했다면 PR을 열어 이 목록에 추가해 주세요.

## 더 많은 선택

다음 프로젝트들은 CLIProxyAPI의 포트이거나 영감을 받았습니다:

### [9Router](https://github.com/decolua/9router)

CLIProxyAPI에서 영감을 받은 Next.js 구현. 설치·사용이 쉽고, 자체 포맷 변환(OpenAI/Claude/Gemini/Ollama), 자동 폴백 콤보 시스템, 지수 백오프 다중 계정 관리, Next.js 웹 대시보드, CLI 도구(Cursor, Claude Code, Cline, RooCode) 지원 — API 키 불필요.

### [OmniRoute](https://github.com/diegosouzapw/OmniRoute)

코딩을 멈추지 마세요. 자동 폴백으로 무료·저비용 AI 모델로 스마트 라우팅.

OmniRoute는 멀티 프로바이더 LLM용 AI 게이트웨이입니다: 스마트 라우팅, 로드 밸런싱, 재시도, 폴백을 갖춘 OpenAI 호환 엔드포인트. 정책, 레이트 리밋, 캐싱, 관측성을 추가해 안정적이고 비용을 고려한 추론을 제공합니다.

### [Codex Switch](https://github.com/9ycrooked/CodexSwitch)

Tauri 2 + Vue 3로 만든 여러 OpenAI Codex 데스크톱 계정 관리 도구. 저장된 ChatGPT/Codex 인증 프로필 간 전환, 5시간·주간 쿼터 실시간 확인, 토큰 상태 검증, 활성 계정 상세 조회, 수동 복사 없이 auth.json 임포트·저장 지원.

> [!NOTE]
> CLIProxyAPI의 포트나 영감을 받은 프로젝트를 개발했다면 PR을 열어 이 목록에 추가해 주세요.

## 라이선스

이 프로젝트는 MIT 라이선스로 배포됩니다. 자세한 내용은 [LICENSE](LICENSE) 파일을 참조하세요.
