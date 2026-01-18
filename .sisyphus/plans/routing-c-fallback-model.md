# Plan C: Fallback Model

## Context

### Original Request
특정 모델의 모든 auth가 freeze 상태이거나 없는 경우, 설정된 대체 모델로 자동 fallback.

설정 예시:
```yaml
routing:
  fallback-models:
    gpt-4o: claude-sonnet-4-20250514
    opus: sonnet
    sonnet: glm-4.7
```

### Interview Summary
**Key Discussions**:
- Fallback 트리거: 429/401/5xx 에러만 (MarkResult() 기반)
- Fallback 후 복구: 일시적 (다음 요청에서 원래 모델 시도)
- Streaming fallback: 응답 시작 전에만
- Fallback 범위: chat/completion 엔드포인트만
- 순환 감지: visited set으로 구현

**Research Findings**:
- `sdk/cliproxy/auth/conductor.go:267-300`: Execute() retry loop
- `sdk/cliproxy/auth/conductor.go:337-370`: ExecuteStream() retry loop
- `sdk/cliproxy/auth/conductor.go:909-1025`: MarkResult() 에러 처리
- `sdk/api/handlers/handlers.go:382-419`: ExecuteWithAuthManager() - chat/completion용
- `sdk/api/handlers/handlers.go:423-456`: ExecuteCountWithAuthManager() - count-tokens용

### Metis Review
**Identified Gaps** (addressed):
- Fallback 트리거 조건 구체화 → 429/401/5xx만
- Fallback 모델도 실패 시 → 에러 반환 (Chain 없으면)
- Streaming fallback → 응답 시작 전에만

**Dependencies**:
- 계획 A (Key-based Routing Mode) 완료 후 진행

### Endpoint-specific Fallback 메커니즘 (핵심 설계 결정)

**구현 접근 방식: 별도 메서드 사용**
- **Execute()**: fallback 로직 포함 → chat/completion에서 호출
- **ExecuteCount()**: fallback 로직 없음 (기존 동작 유지) → count-tokens에서 호출
- **이유**: 기존 코드 구조에서 이미 두 메서드가 분리되어 있음. Options 변경 없이 자연스럽게 endpoint별 동작 차별화 가능.

**Fallback 로직 통합 위치**:
```
Execute() 메서드 내부:
  1. executeMixedOnce() 호출
  2. 모든 auth 실패 (lastErr != nil) 시
  3. lastErr의 상태 코드가 429/401/5xx인지 확인
  4. fallbackModels[originalModel] 조회
  5. fallback 모델 존재 + visited에 없으면 재귀적으로 Execute() 호출
  6. visited에 있으면 순환 에러 반환
```

---

## Work Objectives

### Core Objective
특정 모델의 모든 auth가 실패(429/401/5xx)하면 설정된 fallback 모델로 자동 전환하여 요청 처리.

### Concrete Deliverables
- `internal/config/config.go`: `RoutingConfig.FallbackModels` 필드 추가
- `sdk/cliproxy/auth/conductor.go`: Manager에 fallbackModels 필드 + SetFallbackModels() + Execute()/ExecuteStream() 수정
- `sdk/cliproxy/service.go`: 핫 리로드 시 SetFallbackModels() 호출
- `config.example.yaml`: 새 설정 문서화

### Definition of Done
- [x] `routing.fallback-models` 설정 시 원래 모델 실패 → fallback 모델 자동 전환
- [x] 순환 감지 작동 (A → B → A 시 에러)
- [x] chat/completion 엔드포인트에서만 작동 (Execute() 메서드)
- [x] count-tokens에서는 fallback 미작동 (ExecuteCount() 메서드)
- [x] Streaming 응답 시작 전에만 fallback

### Must Have
- `FallbackModels map[string]string` 필드
- Manager.fallbackModels atomic.Value + SetFallbackModels()
- Execute(), ExecuteStream()에 fallback 로직
- 순환 감지 (visited set)
- 429/401/5xx 에러에서만 트리거

### Must NOT Have (Guardrails)
- ❌ ExecuteCount()에 fallback 로직 추가 (count-tokens용)
- ❌ 스트리밍 중간에 fallback
- ❌ 기존 cooldown 로직 수정
- ❌ Options struct 변경
- ❌ 메트릭/모니터링 추가
- ❌ 관리 API 추가

---

## Verification Strategy (MANDATORY)

### Test Decision
- **Infrastructure exists**: YES (Go test)
- **User wants tests**: YES (TDD)
- **Framework**: `go test`

### TDD Pattern
Each TODO follows RED-GREEN-REFACTOR.

---

## Task Flow

```
Task 1 (Config) → Task 2 (Manager Fields) → Task 3 (Execute Fallback) → Task 4 (Cycle Detection) → Task 5 (Wiring) → Task 6 (Example)
```

## Parallelization

| Task | Depends On | Reason |
|------|------------|--------|
| 1 | Plan A | Config struct 확장 |
| 2 | 1 | FallbackModels 참조 필요 |
| 3 | 2 | Manager fallback 설정 필요 |
| 4 | 3 | Fallback 로직에 cycle detection 통합 |
| 5 | 4 | 완성된 fallback 로직 연결 |
| 6 | 5 | 문서화 |

---

## TODOs

- [x] 1. Add `FallbackModels` field to RoutingConfig

  **What to do**:
  - `RoutingConfig` struct에 `FallbackModels map[string]string` 필드 추가
  - YAML 태그: `yaml:"fallback-models,omitempty"`
  - JSON 태그: `json:"fallback-models,omitempty"`
  - 키: 원래 모델명, 값: fallback 모델명

  **구체적 코드 변경**:
  ```go
  // internal/config/config.go:154-160 (Plan A 이후)
  // 변경 전:
  type RoutingConfig struct {
      Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
      Mode     string `yaml:"mode,omitempty" json:"mode,omitempty"`
  }
  
  // 변경 후:
  type RoutingConfig struct {
      Strategy       string            `yaml:"strategy,omitempty" json:"strategy,omitempty"`
      Mode           string            `yaml:"mode,omitempty" json:"mode,omitempty"`
      FallbackModels map[string]string `yaml:"fallback-models,omitempty" json:"fallback-models,omitempty"`
  }
  ```

  **Must NOT do**:
  - `Mode`, `Strategy` 필드 변경
  - 새로운 struct 생성

  **Parallelizable**: NO (첫 번째 태스크)

  **References**:
  
  **Pattern References**:
  - `internal/config/config.go:154-159` - RoutingConfig 현재 구조 (Plan A에서 Mode 추가됨)
  - `internal/config/config.go:98-108` - OAuthModelMappings 맵 패턴 참고
  
  **Test References**:
  - `internal/config/routing_config_test.go` - Plan A에서 생성된 테스트 파일에 추가

  **Acceptance Criteria**:
  
  - [ ] Test: `routing.fallback-models` 맵 파싱 확인
  - [ ] Test: 빈 맵일 때 nil 또는 빈 맵
  - [ ] Test: 여러 항목 파싱: `{gpt-4o: claude, opus: sonnet}`
  - [ ] `go test ./internal/config/...` → PASS

  **Commit**: YES
  - Message: `feat(config): add routing.fallback-models field`
  - Files: `internal/config/config.go`, `internal/config/routing_config_test.go`
  - Pre-commit: `go test ./internal/config/...`

---

- [x] 2. Add fallbackModels field and SetFallbackModels() to Manager

  **What to do**:
  - `Manager` struct에 `fallbackModels atomic.Value` 필드 추가
  - `SetFallbackModels(models map[string]string)` 메서드 추가
  - `getFallbackModel(originalModel string) (string, bool)` 헬퍼 메서드 추가

  **구체적 코드 변경**:
  ```go
  // sdk/cliproxy/auth/conductor.go:106-128 (Manager struct에 추가)
  type Manager struct {
      // ... 기존 필드들 ...
      
      // Fallback models configuration (atomic for hot reload)
      fallbackModels atomic.Value  // stores map[string]string
  }
  
  // SetFallbackModels 메서드 추가 (line ~200 근처, SetRetryConfig 패턴 따라서)
  func (m *Manager) SetFallbackModels(models map[string]string) {
      if m == nil {
          return
      }
      if models == nil {
          models = make(map[string]string)
      }
      m.fallbackModels.Store(models)
  }
  
  // getFallbackModel 헬퍼 메서드 추가
  func (m *Manager) getFallbackModel(originalModel string) (string, bool) {
      if m == nil {
          return "", false
      }
      models, ok := m.fallbackModels.Load().(map[string]string)
      if !ok || models == nil {
          return "", false
      }
      fallback, exists := models[originalModel]
      return fallback, exists && fallback != ""
  }
  ```

  **Must NOT do**:
  - 기존 atomic.Value 필드 변경
  - NewManager() 시그니처 변경

  **Parallelizable**: NO (Task 1 의존)

  **References**:
  
  **Pattern References**:
  - `sdk/cliproxy/auth/conductor.go:106-128` - Manager struct 현재 구조
  - `sdk/cliproxy/auth/conductor.go:120-121` - modelNameMappings atomic.Value 패턴
  - `sdk/cliproxy/auth/conductor.go:174-187` - SetRetryConfig() 메서드 패턴
  
  **Test References**:
  - `sdk/cliproxy/auth/conductor_test.go` (있으면) 또는 새로 생성

  **Acceptance Criteria**:
  
  - [ ] Test: SetFallbackModels(nil) 시 빈 맵으로 저장
  - [ ] Test: SetFallbackModels({gpt-4o: claude}) 후 getFallbackModel("gpt-4o") → "claude", true
  - [ ] Test: getFallbackModel("unknown") → "", false
  - [ ] `go test ./sdk/cliproxy/auth/...` → PASS

  **Commit**: YES
  - Message: `feat(conductor): add fallbackModels field and SetFallbackModels method`
  - Files: `sdk/cliproxy/auth/conductor.go`, `sdk/cliproxy/auth/conductor_test.go`
  - Pre-commit: `go test ./sdk/cliproxy/auth/...`

---

- [x] 3. Implement fallback logic in Execute() and ExecuteStream()

  **What to do**:
  - `Execute()` 메서드 수정: 실패 시 fallback 모델로 재시도
  - `ExecuteStream()` 메서드 수정: 동일한 fallback 로직
  - `executeWithFallback()` 내부 헬퍼 함수 추가 (재귀 방지를 위한 visited set 파라미터)

  **구체적 코드 변경**:
  ```go
  // sdk/cliproxy/auth/conductor.go:267-300 (Execute() 수정)
  // 변경 전:
  func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
      // ... 기존 구현 ...
  }
  
  // 변경 후:
  func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
      // 첫 호출 시 visited set 초기화
      visited := make(map[string]struct{})
      return m.executeWithFallback(ctx, providers, req, opts, visited)
  }
  
  // 새로운 헬퍼 함수 추가
  func (m *Manager) executeWithFallback(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, visited map[string]struct{}) (cliproxyexecutor.Response, error) {
      originalModel := req.Model
      
      // 순환 감지
      if _, seen := visited[originalModel]; seen {
          return cliproxyexecutor.Response{}, &Error{Code: "fallback_cycle", Message: fmt.Sprintf("fallback cycle detected: model %s already tried", originalModel)}
      }
      visited[originalModel] = struct{}{}
      
      // 기존 Execute 로직 (executeMixedOnce 호출 포함)
      // ... (기존 267-300 라인의 로직) ...
      
      // 모든 재시도 실패 후 fallback 체크
      if lastErr != nil {
          if shouldTriggerFallback(lastErr) {
              if fallbackModel, ok := m.getFallbackModel(originalModel); ok {
                  log.Debugf("fallback from %s to %s", originalModel, fallbackModel)
                  
                  // fallback 모델의 provider 찾기
                  fallbackProviders := util.GetProviderName(fallbackModel)
                  if len(fallbackProviders) > 0 {
                      fallbackReq := req
                      fallbackReq.Model = fallbackModel
                      return m.executeWithFallback(ctx, fallbackProviders, fallbackReq, opts, visited)
                  }
              }
          }
          return cliproxyexecutor.Response{}, lastErr
      }
      // ... 기존 성공 반환 로직 ...
  }
  
  // shouldTriggerFallback 헬퍼 함수 추가
  func shouldTriggerFallback(err error) bool {
      status := statusCodeFromError(err)
      // 429 (quota), 401 (unauthorized), 5xx (server error)만 fallback 트리거
      return status == 429 || status == 401 || (status >= 500 && status < 600)
  }
  ```

  **Must NOT do**:
  - ExecuteCount() 수정 (fallback 없음)
  - executeMixedOnce() 내부 로직 변경
  - MarkResult() 로직 변경

  **Parallelizable**: NO (Task 2 의존)

  **References**:
  
  **Pattern References**:
  - `sdk/cliproxy/auth/conductor.go:267-300` - Execute() 현재 구현
  - `sdk/cliproxy/auth/conductor.go:337-370` - ExecuteStream() 현재 구현
  - `sdk/cliproxy/auth/conductor.go:1164-1176` - statusCodeFromError() 구현
  - `internal/util/provider.go:15-52` - GetProviderName() 구현
  
  **API/Type References**:
  - `sdk/cliproxy/auth/conductor.go:62-76` - Result struct
  - `sdk/cliproxy/executor/types.go` - Request, Response, Options

  **Acceptance Criteria**:
  
  - [ ] Test: 원래 모델 성공 시 fallback 미사용
  - [ ] Test: 원래 모델 429 에러 시 fallback 시도
  - [ ] Test: 원래 모델 401 에러 시 fallback 시도
  - [ ] Test: 원래 모델 5xx 에러 시 fallback 시도
  - [ ] Test: 400 에러 시 fallback 미시도 (클라이언트 에러)
  - [ ] Test: fallback 모델도 실패 시 최종 에러 반환
  - [ ] Test: ExecuteCount()는 fallback 없이 기존 동작 유지
  - [ ] `go test ./sdk/cliproxy/auth/...` → PASS

  **Commit**: YES
  - Message: `feat(conductor): implement model fallback in Execute and ExecuteStream`
  - Files: `sdk/cliproxy/auth/conductor.go`, `sdk/cliproxy/auth/conductor_test.go`
  - Pre-commit: `go test ./sdk/cliproxy/auth/...`

---

- [x] 4. Implement cycle detection (integrated in Task 3)

  **What to do**:
  - Task 3의 `visited` set이 순환 감지 역할을 함
  - 테스트만 추가로 작성

  **Acceptance Criteria**:
  
  - [ ] Test: A → B fallback 성공 (B에서 성공)
  - [ ] Test: A → B → A 순환 시 "fallback cycle detected" 에러 반환
  - [ ] Test: visited가 요청 간에 공유되지 않음 (각 요청마다 새로운 visited set)
  - [ ] `go test ./sdk/cliproxy/auth/...` → PASS

  **Commit**: NO (Task 3에 포함)

---

- [x] 5. Wire fallback config to Conductor

  **What to do**:
  - `sdk/cliproxy/builder.go`에서 service 초기화 시 `SetFallbackModels()` 호출
  - `sdk/cliproxy/service.go`에서 핫 리로드 시 `SetFallbackModels()` 호출

  **구체적 코드 변경**:
  ```go
  // sdk/cliproxy/builder.go:218 근처에 추가
  coreManager.SetOAuthModelMappings(b.cfg.OAuthModelMappings)
  coreManager.SetFallbackModels(b.cfg.Routing.FallbackModels)  // 새로 추가
  
  // sdk/cliproxy/service.go:560 근처에 추가 (핫 리로드 콜백 내부)
  if s.coreManager != nil {
      s.coreManager.SetOAuthModelMappings(newCfg.OAuthModelMappings)
      s.coreManager.SetFallbackModels(newCfg.Routing.FallbackModels)  // 새로 추가
  }
  ```

  **Must NOT do**:
  - 새로운 API 엔드포인트 추가
  - Options struct 변경

  **Parallelizable**: NO (Task 3 의존)

  **References**:
  
  **Pattern References**:
  - `sdk/cliproxy/builder.go:218` - SetOAuthModelMappings() 호출 패턴
  - `sdk/cliproxy/service.go:559-561` - 핫 리로드 시 SetOAuthModelMappings() 호출 패턴

  **Acceptance Criteria**:
  
  - [ ] 빌드 성공: `go build ./...`
  - [ ] 통합 테스트: config에 fallback-models 설정 후 서비스 시작 → 설정 반영 확인
  - [ ] 통합 테스트: config 파일에서 fallback-models 변경 → 핫 리로드 후 새 설정 반영
  - [ ] `go test ./...` → PASS

  **Commit**: YES
  - Message: `feat(service): wire fallback-models config to conductor`
  - Files: `sdk/cliproxy/builder.go`, `sdk/cliproxy/service.go`
  - Pre-commit: `go build ./... && go test ./sdk/cliproxy/...`

---

- [x] 6. Document in config.example.yaml

  **What to do**:
  - `config.example.yaml`의 `routing:` 섹션에 `fallback-models` 필드 추가
  - 주석으로 설명

  **구체적 코드 변경**:
  ```yaml
  # config.example.yaml:78-85
  routing:
    strategy: "round-robin" # round-robin (default), fill-first
    # mode: "key-based" # (optional) key-based: ignore provider, round-robin by model only
    # fallback-models: # (optional) automatic model fallback on 429/401/5xx errors
    #   gpt-4o: claude-sonnet-4-20250514  # gpt-4o fails → try claude
    #   opus: sonnet                       # opus fails → try sonnet
    # Note: Fallback only applies to chat/completion endpoints, not count-tokens
  ```

  **Must NOT do**:
  - 다른 설정 섹션 변경

  **Parallelizable**: NO (Task 5 의존)

  **References**:
  
  **Pattern References**:
  - `config.example.yaml:78-80` - routing 섹션

  **Acceptance Criteria**:
  
  - [ ] `routing.fallback-models` 필드가 주석으로 문서화됨
  - [ ] fallback이 chat/completion에서만 작동함을 명시
  - [ ] YAML 문법 오류 없음

  **Commit**: YES
  - Message: `docs(config): document routing.fallback-models setting`
  - Files: `config.example.yaml`
  - Pre-commit: N/A

---

## Expected Behavior Examples

### Scenario 1: 모든 auth가 429 에러
1. Client 요청: model=gpt-4o
2. Manager.Execute() 호출 (visited={})
3. executeMixedOnce(): gpt-4o의 모든 auth 실행 → 모두 429 에러
4. shouldTriggerFallback(429) → true
5. getFallbackModel("gpt-4o") → "claude-sonnet-4", true
6. Log: "fallback from gpt-4o to claude-sonnet-4"
7. Manager.executeWithFallback() 재귀 호출 (visited={gpt-4o})
8. claude-sonnet-4 성공 → Response 반환

### Scenario 2: 순환 감지
1. Config: fallback-models: {A: B, B: A}
2. Client 요청: model=A
3. Manager.Execute() → visited={A}
4. A의 모든 auth 실패 (429) → B로 fallback
5. executeWithFallback(B) → visited={A, B}
6. B의 모든 auth 실패 (429) → A로 fallback 시도
7. visited에 A가 이미 있음 → Error "fallback cycle detected"

### Scenario 3: count-tokens는 fallback 없음
1. Client 요청: POST /v1/tokens/count, model=gpt-4o
2. Handler: ExecuteCountWithAuthManager() 호출
3. Manager.ExecuteCount() 호출 (fallback 로직 없음)
4. gpt-4o의 모든 auth 실패 → 에러 반환 (fallback 시도 없음)

---

## Commit Strategy

| After Task | Message | Files | Verification |
|------------|---------|-------|--------------|
| 1 | `feat(config): add fallback-models field` | config.go | `go test ./internal/config/...` |
| 2 | `feat(conductor): add SetFallbackModels method` | conductor.go | `go test ./sdk/cliproxy/auth/...` |
| 3 | `feat(conductor): implement fallback in Execute` | conductor.go | `go test ./sdk/cliproxy/auth/...` |
| 5 | `feat(service): wire fallback-models config` | builder.go, service.go | `go test ./...` |
| 6 | `docs(config): document fallback-models` | config.example.yaml | manual |

---

## Success Criteria

### Verification Commands
```bash
# 단위 테스트
go test ./internal/config/... -v
go test ./sdk/cliproxy/auth/... -v

# 통합 테스트
go test ./... -v

# 빌드 확인
go build ./cmd/server
```

### Final Checklist
- [x] fallback-models 설정 시 자동 전환 작동
- [x] 순환 감지 작동 (A → B → A 에러)
- [x] chat/completion에서만 fallback (Execute())
- [x] count-tokens에서 fallback 없음 (ExecuteCount())
- [x] 429/401/5xx 에러에서만 트리거
- [x] 핫 리로드 시 fallback 설정 반영
- [x] 모든 테스트 통과
