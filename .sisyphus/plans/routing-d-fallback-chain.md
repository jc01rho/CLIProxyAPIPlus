# Plan D: Fallback Chain

## ⚠️ PREREQUISITES (MUST READ)

**이 계획은 다음 계획들이 완료된 후에만 실행 가능합니다:**

| 선행 계획 | 파일 | 상태 확인 방법 |
|-----------|------|---------------|
| Plan A | `routing-a-key-based.md` | `RoutingConfig.Mode` 필드 존재 확인 |
| Plan C | `routing-c-fallback-model.md` | `RoutingConfig.FallbackModels` 필드 + Manager.executeWithFallback() 존재 확인 |

**Plan C가 구현되지 않은 상태에서 Plan D를 시작하지 마세요!**

Plan C 완료 후 예상 코드 상태:
- `internal/config/config.go`: RoutingConfig에 `Mode`, `FallbackModels` 필드 존재
- `sdk/cliproxy/auth/conductor.go`: Manager에 `fallbackModels atomic.Value`, `SetFallbackModels()`, `executeWithFallback()` 존재

---

## Context

### Original Request
`fallback-models`에 지정되지 않은 모델을 위한 일반 fallback chain 설정.

설정 예시:
```yaml
routing:
  fallback-chain:
    - glm-4.7
    - grok-code-fast-1
```

### Interview Summary
**Key Discussions**:
- Fallback chain은 `fallback-models`에 없는 모델에 적용
- 최대 3단계까지 시도 (설정 가능)
- 순환 감지는 Plan C에서 구현한 것 재사용
- chat/completion 엔드포인트에서만 작동

**Research Findings**:
- Plan C에서 구현한 fallback 로직 확장
- `fallback-models` 체크 후 `fallback-chain` 체크
- Execute(), ExecuteStream()에만 적용 (ExecuteCount() 제외)

### Metis Review
**Identified Gaps** (addressed):
- Chain 최대 길이 → 3단계 (FallbackMaxDepth로 설정 가능)
- Chain과 fallback-models 우선순위 → fallback-models 먼저

**Dependencies**:
- 계획 A (Key-based Routing Mode) 완료
- 계획 C (Fallback Model) 완료

### Fallback 우선순위

```
1. fallback-models[originalModel] 조회
   → 있으면 그 모델로 fallback
2. fallback-chain 순서대로 시도
   → fallback-chain[0] → fallback-chain[1] → ...
3. visited.size >= maxDepth이면 중단
4. 모두 실패 시 최종 에러 반환
```

---

## Work Objectives

### Core Objective
`fallback-models`에 지정되지 않은 모든 모델에 대해 `fallback-chain` 순서대로 fallback 시도.

### Concrete Deliverables
- `internal/config/config.go`: `RoutingConfig.FallbackChain`, `FallbackMaxDepth` 필드 추가
- `sdk/cliproxy/auth/conductor.go`: Manager에 chain fallback 로직 추가
- `sdk/cliproxy/builder.go`, `sdk/cliproxy/service.go`: 핫 리로드 연결
- `config.example.yaml`: 새 설정 문서화

### Definition of Done
- [x] `routing.fallback-chain` 설정 시 chain 순서대로 fallback
- [x] 최대 3단계까지 시도 (기본값, `fallback-max-depth`로 설정 가능)
- [x] `fallback-models`가 있으면 그것 먼저, 없으면 chain 사용
- [x] 순환 감지 작동

### Must Have
- `FallbackChain []string` 필드
- `FallbackMaxDepth int` 필드 (기본값 3)
- Chain fallback 로직 (fallback-models 다음 우선순위)
- Plan C의 순환 감지 및 visited set 재사용

### Must NOT Have (Guardrails)
- ❌ 무한 chain 허용
- ❌ ExecuteCount()에 fallback 로직 추가
- ❌ 스트리밍 중간에 fallback
- ❌ 메트릭/모니터링 추가
- ❌ fallback-models 로직 변경

---

## Verification Strategy (MANDATORY)

### Test Decision
- **Infrastructure exists**: YES (Go test)
- **User wants tests**: YES (TDD)
- **Framework**: `go test`

---

## Task Flow

```
Task 1 (Config) → Task 2 (Manager Fields) → Task 3 (Chain Logic) → Task 4 (Wiring) → Task 5 (Example)
```

## Parallelization

| Task | Depends On | Reason |
|------|------------|--------|
| 1 | Plan C | Config struct 확장 |
| 2 | 1 | FallbackChain 참조 필요 |
| 3 | 2 | Manager chain 설정 필요 |
| 4 | 3 | 완성된 chain 로직 연결 |
| 5 | 4 | 문서화 |

---

## TODOs

- [x] 1. Add `FallbackChain` and `FallbackMaxDepth` fields to RoutingConfig

  **What to do**:
  - `RoutingConfig` struct에 추가:
    - `FallbackChain []string` - YAML: `yaml:"fallback-chain,omitempty"`
    - `FallbackMaxDepth int` - YAML: `yaml:"fallback-max-depth,omitempty"` (기본값 3)
  - 설정 sanitize에서 기본값 설정: `FallbackMaxDepth = 3` (0이면)

  **구체적 코드 변경**:
  ```go
  // internal/config/config.go:154-163 (Plan C 이후)
  // 변경 전:
  type RoutingConfig struct {
      Strategy       string            `yaml:"strategy,omitempty" json:"strategy,omitempty"`
      Mode           string            `yaml:"mode,omitempty" json:"mode,omitempty"`
      FallbackModels map[string]string `yaml:"fallback-models,omitempty" json:"fallback-models,omitempty"`
  }
  
  // 변경 후:
  type RoutingConfig struct {
      Strategy         string            `yaml:"strategy,omitempty" json:"strategy,omitempty"`
      Mode             string            `yaml:"mode,omitempty" json:"mode,omitempty"`
      FallbackModels   map[string]string `yaml:"fallback-models,omitempty" json:"fallback-models,omitempty"`
      FallbackChain    []string          `yaml:"fallback-chain,omitempty" json:"fallback-chain,omitempty"`
      FallbackMaxDepth int               `yaml:"fallback-max-depth,omitempty" json:"fallback-max-depth,omitempty"`
  }
  ```

  **기본값 설정** (config.go의 sanitize 또는 LoadConfig 영역):
  ```go
  // LoadConfigOptional() 내에서 또는 별도 sanitize 함수에서
  if cfg.Routing.FallbackMaxDepth <= 0 {
      cfg.Routing.FallbackMaxDepth = 3
  }
  ```

  **Must NOT do**:
  - `FallbackModels` 필드 변경
  - 기존 필드 수정

  **Parallelizable**: NO (첫 번째 태스크)

  **References**:
  
  **Pattern References**:
  - `internal/config/config.go:154-163` - RoutingConfig 현재 구조 (Plan C에서 확장됨)
  - `internal/config/config.go:72-76` - GeminiKey []GeminiKey 슬라이스 패턴
  - `internal/config/config.go:59-61` - RequestRetry, MaxRetryInterval int 패턴
  - `internal/config/config.go:800-850` (예상) - sanitize 함수들 패턴
  
  **Test References**:
  - `internal/config/routing_config_test.go` - Plan A, C에서 생성된 테스트 파일에 추가

  **Acceptance Criteria**:
  
  - [ ] Test: `routing.fallback-chain` 배열 파싱 확인
  - [ ] Test: `routing.fallback-max-depth` 파싱 확인
  - [ ] Test: max-depth 미설정 시 기본값 3
  - [ ] Test: max-depth가 0이면 기본값 3으로 설정
  - [ ] Test: 빈 chain일 때 nil 또는 빈 슬라이스
  - [ ] `go test ./internal/config/...` → PASS

  **Commit**: YES
  - Message: `feat(config): add routing.fallback-chain and fallback-max-depth fields`
  - Files: `internal/config/config.go`, `internal/config/routing_config_test.go`
  - Pre-commit: `go test ./internal/config/...`

---

- [x] 2. Add fallbackChain and fallbackMaxDepth fields to Manager

  **What to do**:
  - `Manager` struct에 추가:
    - `fallbackChain atomic.Value` (stores []string)
    - `fallbackMaxDepth atomic.Int32`
  - `SetFallbackChain(chain []string, maxDepth int)` 메서드 추가
  - `getFallbackChain() []string` 헬퍼 메서드 추가
  - `getFallbackMaxDepth() int` 헬퍼 메서드 추가

  **구체적 코드 변경**:
  ```go
  // sdk/cliproxy/auth/conductor.go:106-130 (Manager struct에 추가)
  type Manager struct {
      // ... 기존 필드들 ...
      fallbackModels   atomic.Value  // stores map[string]string (Plan C)
      fallbackChain    atomic.Value  // stores []string (Plan D)
      fallbackMaxDepth atomic.Int32  // default 3 (Plan D)
  }
  
  // SetFallbackChain 메서드 추가
  func (m *Manager) SetFallbackChain(chain []string, maxDepth int) {
      if m == nil {
          return
      }
      if chain == nil {
          chain = []string{}
      }
      m.fallbackChain.Store(chain)
      if maxDepth <= 0 {
          maxDepth = 3
      }
      m.fallbackMaxDepth.Store(int32(maxDepth))
  }
  
  // getFallbackChain 헬퍼 메서드 추가
  func (m *Manager) getFallbackChain() []string {
      if m == nil {
          return nil
      }
      chain, ok := m.fallbackChain.Load().([]string)
      if !ok {
          return nil
      }
      return chain
  }
  
  // getFallbackMaxDepth 헬퍼 메서드 추가
  func (m *Manager) getFallbackMaxDepth() int {
      if m == nil {
          return 3
      }
      depth := m.fallbackMaxDepth.Load()
      if depth <= 0 {
          return 3
      }
      return int(depth)
  }
  ```

  **Must NOT do**:
  - Plan C의 fallbackModels 필드/메서드 변경
  - NewManager() 시그니처 변경

  **Parallelizable**: NO (Task 1 의존)

  **References**:
  
  **Pattern References**:
  - `sdk/cliproxy/auth/conductor.go:106-130` - Manager struct 현재 구조 (Plan C에서 확장됨)
  - `sdk/cliproxy/auth/conductor.go:116-118` - requestRetry, maxRetryInterval atomic 패턴
  - Plan C에서 추가한 SetFallbackModels() 메서드 패턴
  
  **Test References**:
  - `sdk/cliproxy/auth/conductor_test.go` - Plan C에서 생성/수정된 테스트 파일에 추가

  **Acceptance Criteria**:
  
  - [ ] Test: SetFallbackChain(nil, 0) 시 빈 슬라이스, maxDepth=3
  - [ ] Test: SetFallbackChain(["a", "b"], 5) 후 getFallbackChain() → ["a", "b"]
  - [ ] Test: getFallbackMaxDepth() → 5
  - [ ] Test: maxDepth=0 설정 시 기본값 3
  - [ ] `go test ./sdk/cliproxy/auth/...` → PASS

  **Commit**: YES
  - Message: `feat(conductor): add fallbackChain and fallbackMaxDepth fields`
  - Files: `sdk/cliproxy/auth/conductor.go`, `sdk/cliproxy/auth/conductor_test.go`
  - Pre-commit: `go test ./sdk/cliproxy/auth/...`

---

- [x] 3. Extend executeWithFallback() to support chain fallback

  **What to do**:
  - Plan C에서 구현한 `executeWithFallback()` 수정
  - fallback-models에 없으면 fallback-chain 순서대로 시도
  - visited.size >= maxDepth이면 중단

  **구체적 코드 변경**:
  ```go
  // sdk/cliproxy/auth/conductor.go의 executeWithFallback() 수정
  func (m *Manager) executeWithFallback(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, visited map[string]struct{}) (cliproxyexecutor.Response, error) {
      originalModel := req.Model
      
      // 순환 감지
      if _, seen := visited[originalModel]; seen {
          return cliproxyexecutor.Response{}, &Error{Code: "fallback_cycle", Message: fmt.Sprintf("fallback cycle detected: model %s already tried", originalModel)}
      }
      visited[originalModel] = struct{}{}
      
      // 기존 Execute 로직 (executeMixedOnce 호출 포함)
      // ... (Plan C의 기존 로직) ...
      
      // 모든 재시도 실패 후 fallback 체크
      if lastErr != nil {
          if shouldTriggerFallback(lastErr) {
              // 1단계: fallback-models 체크 (Plan C 로직)
              if fallbackModel, ok := m.getFallbackModel(originalModel); ok {
                  log.Debugf("fallback from %s to %s (via fallback-models)", originalModel, fallbackModel)
                  fallbackProviders := util.GetProviderName(fallbackModel)
                  if len(fallbackProviders) > 0 {
                      fallbackReq := req
                      fallbackReq.Model = fallbackModel
                      return m.executeWithFallback(ctx, fallbackProviders, fallbackReq, opts, visited)
                  }
              }
              
              // 2단계: fallback-chain 체크 (Plan D 로직)
              maxDepth := m.getFallbackMaxDepth()
              if len(visited) < maxDepth {
                  chain := m.getFallbackChain()
                  for _, chainModel := range chain {
                      // 이미 시도한 모델은 건너뛰기
                      if _, tried := visited[chainModel]; tried {
                          continue
                      }
                      log.Debugf("fallback from %s to %s (via fallback-chain, depth %d/%d)", originalModel, chainModel, len(visited), maxDepth)
                      chainProviders := util.GetProviderName(chainModel)
                      if len(chainProviders) > 0 {
                          chainReq := req
                          chainReq.Model = chainModel
                          return m.executeWithFallback(ctx, chainProviders, chainReq, opts, visited)
                      }
                  }
              } else {
                  log.Debugf("fallback depth limit reached (%d/%d), not trying chain", len(visited), maxDepth)
              }
          }
          return cliproxyexecutor.Response{}, lastErr
      }
      // ... 기존 성공 반환 로직 ...
  }
  ```

  **Must NOT do**:
  - fallback-models 우선순위 변경
  - maxDepth 무시
  - ExecuteCount()에 chain 로직 추가

  **Parallelizable**: NO (Task 2 의존)

  **References**:
  
  **Pattern References**:
  - Plan C에서 구현한 `executeWithFallback()` - 현재 구현
  - `internal/util/provider.go:15-52` - GetProviderName() 구현
  
  **API/Type References**:
  - `sdk/cliproxy/executor/types.go` - Request, Response, Options

  **Acceptance Criteria**:
  
  - [ ] Test: fallback-models에 있으면 chain 무시
  - [ ] Test: fallback-models에 없으면 chain 순서대로 시도
  - [ ] Test: chain[0] 실패 시 chain[1] 시도
  - [ ] Test: chain의 모든 모델 실패 시 최종 에러 반환
  - [ ] Test: maxDepth=2 설정 시 2단계까지만 시도
  - [ ] Test: chain 중간에 성공하면 중단
  - [ ] Test: chain에서 이미 시도한 모델은 건너뜀
  - [ ] `go test ./sdk/cliproxy/auth/...` → PASS

  **Commit**: YES
  - Message: `feat(conductor): implement fallback chain logic`
  - Files: `sdk/cliproxy/auth/conductor.go`, `sdk/cliproxy/auth/conductor_test.go`
  - Pre-commit: `go test ./sdk/cliproxy/auth/...`

---

- [x] 4. Wire chain config to Conductor

  **What to do**:
  - `sdk/cliproxy/builder.go`에서 service 초기화 시 `SetFallbackChain()` 호출
  - `sdk/cliproxy/service.go`에서 핫 리로드 시 `SetFallbackChain()` 호출

  **구체적 코드 변경**:
  ```go
  // sdk/cliproxy/builder.go:218-220 근처에 추가
  coreManager.SetOAuthModelMappings(b.cfg.OAuthModelMappings)
  coreManager.SetFallbackModels(b.cfg.Routing.FallbackModels)  // Plan C
  coreManager.SetFallbackChain(b.cfg.Routing.FallbackChain, b.cfg.Routing.FallbackMaxDepth)  // Plan D
  
  // sdk/cliproxy/service.go:560-562 근처에 추가 (핫 리로드 콜백 내부)
  if s.coreManager != nil {
      s.coreManager.SetOAuthModelMappings(newCfg.OAuthModelMappings)
      s.coreManager.SetFallbackModels(newCfg.Routing.FallbackModels)  // Plan C
      s.coreManager.SetFallbackChain(newCfg.Routing.FallbackChain, newCfg.Routing.FallbackMaxDepth)  // Plan D
  }
  ```

  **Must NOT do**:
  - 새로운 API 엔드포인트 추가

  **Parallelizable**: NO (Task 3 의존)

  **References**:
  
  **Pattern References**:
  - `sdk/cliproxy/builder.go:218-220` - SetOAuthModelMappings(), SetFallbackModels() 호출 패턴 (Plan C)
  - `sdk/cliproxy/service.go:559-562` - 핫 리로드 패턴 (Plan C)

  **Acceptance Criteria**:
  
  - [ ] 빌드 성공: `go build ./...`
  - [ ] 통합 테스트: config에 fallback-chain 설정 후 서비스 시작 → 설정 반영
  - [ ] 통합 테스트: config에서 fallback-max-depth 변경 → 핫 리로드 후 반영
  - [ ] 통합 테스트: fallback-models와 fallback-chain 조합 작동
  - [ ] `go test ./...` → PASS

  **Commit**: YES
  - Message: `feat(service): wire fallback-chain config to conductor`
  - Files: `sdk/cliproxy/builder.go`, `sdk/cliproxy/service.go`
  - Pre-commit: `go build ./... && go test ./sdk/cliproxy/...`

---

- [x] 5. Document in config.example.yaml

  **What to do**:
  - `config.example.yaml`의 `routing:` 섹션에 추가:
    - `fallback-chain`: 배열
    - `fallback-max-depth`: 정수 (기본값 3)
  - 주석으로 설명

  **구체적 코드 변경**:
  ```yaml
  # config.example.yaml:78-92
  routing:
    strategy: "round-robin" # round-robin (default), fill-first
    # mode: "key-based" # (optional) key-based: ignore provider, round-robin by model only
    # fallback-models: # (optional) automatic model fallback on 429/401/5xx errors
    #   gpt-4o: claude-sonnet-4-20250514  # gpt-4o fails → try claude
    #   opus: sonnet                       # opus fails → try sonnet
    # fallback-chain: # (optional) general fallback chain for models not in fallback-models
    #   - glm-4.7         # First choice
    #   - grok-code-fast-1  # Second choice
    # fallback-max-depth: 3 # (optional) maximum fallback depth (default: 3)
    # Note: Fallback only applies to chat/completion endpoints, not count-tokens
  ```

  **Must NOT do**:
  - 다른 설정 섹션 변경

  **Parallelizable**: NO (Task 4 의존)

  **References**:
  
  **Pattern References**:
  - `config.example.yaml:78-86` - routing 섹션 (Plan C에서 확장됨)

  **Acceptance Criteria**:
  
  - [ ] `routing.fallback-chain` 필드가 문서화됨
  - [ ] `routing.fallback-max-depth` 필드가 문서화됨
  - [ ] 예시와 주석 포함
  - [ ] YAML 문법 오류 없음

  **Commit**: YES
  - Message: `docs(config): document routing.fallback-chain setting`
  - Files: `config.example.yaml`
  - Pre-commit: N/A

---

## Expected Behavior Examples

### Scenario 1: fallback-models와 fallback-chain 조합
```yaml
routing:
  fallback-models:
    gpt-4o: claude-sonnet-4
  fallback-chain:
    - glm-4.7
    - grok-code-fast-1
```

1. Client 요청: model=gpt-4o
2. gpt-4o 모든 auth 실패 (429)
3. fallback-models["gpt-4o"] = "claude-sonnet-4" → claude로 fallback
4. claude 성공 → Response 반환 (chain 사용 안 함)

### Scenario 2: fallback-models에 없으면 chain 사용
```yaml
routing:
  fallback-models:
    gpt-4o: claude-sonnet-4
  fallback-chain:
    - glm-4.7
    - grok-code-fast-1
```

1. Client 요청: model=unknown-model
2. unknown-model 모든 auth 실패 (429)
3. fallback-models["unknown-model"] = "" → 없음
4. fallback-chain[0] = "glm-4.7" → glm으로 fallback
5. glm 실패 → fallback-chain[1] = "grok" → grok으로 fallback
6. grok 성공 → Response 반환

### Scenario 3: maxDepth 제한
```yaml
routing:
  fallback-chain:
    - model-a
    - model-b
    - model-c
    - model-d
  fallback-max-depth: 2
```

1. Client 요청: model=original
2. original 실패 → chain[0] = "model-a" fallback (visited: {original, model-a}, depth=2)
3. model-a 실패 → depth limit (2) reached, chain 중단
4. Error 반환 (model-b, model-c, model-d는 시도 안 함)

---

## Commit Strategy

| After Task | Message | Files | Verification |
|------------|---------|-------|--------------|
| 1 | `feat(config): add fallback-chain fields` | config.go | `go test ./internal/config/...` |
| 2 | `feat(conductor): add chain fields` | conductor.go | `go test ./sdk/cliproxy/auth/...` |
| 3 | `feat(conductor): implement chain logic` | conductor.go | `go test ./sdk/cliproxy/auth/...` |
| 4 | `feat(service): wire fallback-chain config` | builder.go, service.go | `go test ./...` |
| 5 | `docs(config): document fallback-chain` | config.example.yaml | manual |

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
- [x] fallback-chain 설정 시 순서대로 시도
- [x] fallback-models 우선, chain 후순위
- [x] 최대 3단계 (기본값, fallback-max-depth로 설정 가능)
- [x] chain에서 이미 시도한 모델은 건너뜀
- [x] 핫 리로드 시 chain 설정 반영
- [x] 모든 테스트 통과
