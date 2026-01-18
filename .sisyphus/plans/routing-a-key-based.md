# Plan A: Key-based Routing Mode

## Context

### Original Request
Provider를 무시하고 동일 모델을 지원하는 모든 auth에 대해 round-robin할 수 있는 `key-based` routing mode 추가.

설정 예시:
```yaml
routing:
  mode: key-based
```

### Interview Summary
**Key Discussions**:
- 현재 `RoundRobinSelector.cursors`가 `provider:model` 키 사용 → `key-based` 모드에서는 `model`만 키로 사용
- `pickNextMixed()` 이미 multi-provider 지원 → 설정만 추가하면 됨
- 기존 `Strategy` 필드와 별개로 `Mode` 필드 추가

**Research Findings**:
- `sdk/cliproxy/auth/selector.go:188`: `key := provider + ":" + model`
- `sdk/cliproxy/builder.go:206-212`: selector 생성 - `&coreauth.RoundRobinSelector{}`
- `sdk/cliproxy/service.go:541-548`: 핫 리로드 시 selector 재생성
- `internal/config/config.go:154-159`: `RoutingConfig` struct

### Metis Review
**Identified Gaps** (addressed):
- Key-based 모드에서 사용하지 않는 credential 처리 → 경고 없이 무시 (기존 동작과 동일)
- Key-based와 mixed 혼합 사용 → 전역 설정으로 하나만 선택

---

## Work Objectives

### Core Objective
`routing.mode: key-based` 설정 시 provider를 무시하고 동일 모델을 지원하는 모든 auth에 대해 round-robin 수행.

### Concrete Deliverables
- `internal/config/config.go`: `RoutingConfig.Mode` 필드 추가
- `sdk/cliproxy/auth/selector.go`: `RoundRobinSelector.Mode` 필드 및 `Pick()` 수정
- `sdk/cliproxy/builder.go`: selector 생성 시 mode 설정
- `sdk/cliproxy/service.go`: 핫 리로드 시 mode 반영
- `config.example.yaml`: 새 설정 문서화

### Definition of Done
- [x] `routing.mode: key-based` 설정 시 동일 모델의 모든 credential이 round-robin됨
- [x] `routing.mode: ""` 또는 미설정 시 기존 동작 유지 (backward compatible)
- [x] 핫 리로드 시 mode 변경 반영
- [x] `config.example.yaml`에 새 설정 문서화됨

### Must Have
- `RoutingConfig.Mode` 필드 추가 (`key-based`, 빈 문자열)
- `RoundRobinSelector.Mode` 필드 추가
- `Pick()`에서 mode에 따른 키 생성 분기
- builder.go, service.go에서 mode 설정
- Backward compatibility (기본값은 기존 동작)

### Must NOT Have (Guardrails)
- ❌ 기존 `Strategy` 필드 동작 변경
- ❌ 새로운 API 엔드포인트 추가
- ❌ 메트릭/모니터링 추가
- ❌ `NewRoundRobinSelector()` 생성자 패턴 변경 (Go struct literal 사용)

---

## Verification Strategy (MANDATORY)

### Test Decision
- **Infrastructure exists**: YES (Go test)
- **User wants tests**: YES (TDD)
- **Framework**: `go test`

### TDD Pattern
Each TODO follows RED-GREEN-REFACTOR:
1. **RED**: Write failing test first
2. **GREEN**: Implement minimum code to pass
3. **REFACTOR**: Clean up while keeping green

---

## Task Flow

```
Task 1 (Config) → Task 2 (Selector) → Task 3 (Builder) → Task 4 (Service) → Task 5 (Example)
```

## Parallelization

| Task | Depends On | Reason |
|------|------------|--------|
| 1 | - | Config struct 먼저 |
| 2 | 1 | Mode 값 참조 필요 |
| 3 | 2 | Selector 변경 후 builder 수정 |
| 4 | 3 | Builder 패턴 확인 후 service 수정 |
| 5 | 4 | 모든 구현 완료 후 문서화 |

---

## TODOs

- [x] 1. Add `Mode` field to RoutingConfig

  **What to do**:
  - `RoutingConfig` struct에 `Mode string` 필드 추가
  - YAML 태그: `yaml:"mode,omitempty"`
  - 유효값: `""`, `"key-based"`
  - 기본값: `""` (기존 동작)

  **구체적 코드 변경**:
  ```go
  // internal/config/config.go:154-159
  // 변경 전:
  type RoutingConfig struct {
      Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
  }
  
  // 변경 후:
  type RoutingConfig struct {
      Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
      Mode     string `yaml:"mode,omitempty" json:"mode,omitempty"`
  }
  ```

  **Must NOT do**:
  - `Strategy` 필드 변경
  - 새로운 struct 생성

  **Parallelizable**: NO (첫 번째 태스크)

  **References**:
  
  **Pattern References**:
  - `internal/config/config.go:154-159` - RoutingConfig 현재 구조
  - `internal/config/config.go:63-64` - QuotaExceeded struct 패턴 참고 (유사한 설정 그룹)
  
  **Test References**:
  - 새로 생성: `internal/config/routing_config_test.go` 또는 기존 테스트 파일에 추가
  - 기존 테스트 패턴: `internal/config/` 디렉토리의 `*_test.go` 파일 참고

  **Acceptance Criteria**:
  
  - [ ] Test file created: `internal/config/routing_config_test.go` (새로 생성)
  - [ ] Test: `routing.mode: key-based` 파싱 확인
  - [ ] Test: `routing.mode` 미설정 시 빈 문자열
  - [ ] `go test ./internal/config/...` → PASS

  **Commit**: YES
  - Message: `feat(config): add routing.mode field for key-based routing`
  - Files: `internal/config/config.go`, `internal/config/routing_config_test.go`
  - Pre-commit: `go test ./internal/config/...`

---

- [x] 2. Add `Mode` field to RoundRobinSelector and modify `Pick()`

  **What to do**:
  - `RoundRobinSelector` struct에 `Mode string` 필드 추가
  - `Pick()` 메서드에서 mode에 따라 키 생성 분기

  **구체적 코드 변경**:
  ```go
  // sdk/cliproxy/auth/selector.go:18-22
  // 변경 전:
  type RoundRobinSelector struct {
      mu      sync.Mutex
      cursors map[string]int
  }
  
  // 변경 후:
  type RoundRobinSelector struct {
      mu      sync.Mutex
      cursors map[string]int
      Mode    string  // "key-based" or empty for default behavior
  }
  
  // sdk/cliproxy/auth/selector.go:188
  // 변경 전:
  key := provider + ":" + model
  
  // 변경 후:
  var key string
  if s.Mode == "key-based" {
      key = model
  } else {
      key = provider + ":" + model
  }
  ```

  **Must NOT do**:
  - `FillFirstSelector` 변경
  - `getAvailableAuths()` 로직 변경
  - 생성자 함수 추가 (Go struct literal 사용)

  **Parallelizable**: NO (Task 1 의존)

  **References**:
  
  **Pattern References**:
  - `sdk/cliproxy/auth/selector.go:18-22` - RoundRobinSelector 구조체
  - `sdk/cliproxy/auth/selector.go:179-203` - Pick() 메서드 현재 구현
  - `sdk/cliproxy/auth/selector.go:188` - 현재 키 생성: `key := provider + ":" + model`
  
  **Test References**:
  - 새로 생성: `sdk/cliproxy/auth/selector_test.go`
  - 기존 테스트 패턴: `sdk/cliproxy/auth/conductor_test.go` 참고 (있는 경우)

  **Acceptance Criteria**:
  
  - [ ] Test file created: `sdk/cliproxy/auth/selector_test.go` (새로 생성)
  - [ ] Test: `Mode=""` 시 `provider:model` 키 사용 (기존 동작)
  - [ ] Test: `Mode="key-based"` 시 `model`만 키 사용
  - [ ] Test: key-based 모드에서 다른 provider의 동일 모델 credential이 round-robin됨
  - [ ] `go test ./sdk/cliproxy/auth/...` → PASS

  **Commit**: YES
  - Message: `feat(selector): add Mode field for key-based routing`
  - Files: `sdk/cliproxy/auth/selector.go`, `sdk/cliproxy/auth/selector_test.go`
  - Pre-commit: `go test ./sdk/cliproxy/auth/...`

---

- [x] 3. Wire config Mode to Selector in builder.go

  **What to do**:
  - `sdk/cliproxy/builder.go`에서 selector 생성 시 `Mode` 필드 설정
  - Go struct literal 방식 사용 (`&coreauth.RoundRobinSelector{Mode: mode}`)

  **구체적 코드 변경**:
  ```go
  // sdk/cliproxy/builder.go:202-212
  // 변경 전:
  strategy := ""
  if b.cfg != nil {
      strategy = strings.ToLower(strings.TrimSpace(b.cfg.Routing.Strategy))
  }
  var selector coreauth.Selector
  switch strategy {
  case "fill-first", "fillfirst", "ff":
      selector = &coreauth.FillFirstSelector{}
  default:
      selector = &coreauth.RoundRobinSelector{}
  }
  
  // 변경 후:
  strategy := ""
  mode := ""
  if b.cfg != nil {
      strategy = strings.ToLower(strings.TrimSpace(b.cfg.Routing.Strategy))
      mode = strings.ToLower(strings.TrimSpace(b.cfg.Routing.Mode))
  }
  var selector coreauth.Selector
  switch strategy {
  case "fill-first", "fillfirst", "ff":
      selector = &coreauth.FillFirstSelector{}
  default:
      selector = &coreauth.RoundRobinSelector{Mode: mode}
  }
  ```

  **Must NOT do**:
  - NewManager 시그니처 변경
  - 생성자 함수 추가

  **Parallelizable**: NO (Task 2 의존)

  **References**:
  
  **Pattern References**:
  - `sdk/cliproxy/builder.go:202-214` - 현재 selector 생성 코드
  - `sdk/cliproxy/builder.go:218` - SetOAuthModelMappings() 패턴 참고

  **Acceptance Criteria**:
  
  - [ ] 빌드 성공: `go build ./...`
  - [ ] 기존 테스트 통과: `go test ./sdk/cliproxy/...`
  - [ ] config에서 `routing.mode: key-based` 설정 시 selector.Mode가 "key-based"로 설정됨

  **Commit**: YES
  - Message: `feat(builder): wire routing.mode to RoundRobinSelector`
  - Files: `sdk/cliproxy/builder.go`
  - Pre-commit: `go build ./... && go test ./sdk/cliproxy/...`

---

- [x] 4. Wire config Mode to Selector in service.go (hot reload)

  **What to do**:
  - `sdk/cliproxy/service.go`의 핫 리로드 코드에서 mode 변경 시 selector 재생성
  - strategy 변경뿐만 아니라 mode 변경 시에도 selector 재생성

  **구체적 코드 변경**:
  ```go
  // sdk/cliproxy/service.go:529-550
  // 변경 전:
  nextStrategy := strings.ToLower(strings.TrimSpace(newCfg.Routing.Strategy))
  // ... (strategy normalization) ...
  if s.coreManager != nil && previousStrategy != nextStrategy {
      var selector coreauth.Selector
      switch nextStrategy {
      case "fill-first":
          selector = &coreauth.FillFirstSelector{}
      default:
          selector = &coreauth.RoundRobinSelector{}
      }
      s.coreManager.SetSelector(selector)
      log.Infof("routing strategy updated to %s", nextStrategy)
  }
  
  // 변경 후:
  nextStrategy := strings.ToLower(strings.TrimSpace(newCfg.Routing.Strategy))
  nextMode := strings.ToLower(strings.TrimSpace(newCfg.Routing.Mode))
  // ... (strategy normalization) ...
  previousMode := ""
  if s.cfg != nil {
      previousMode = strings.ToLower(strings.TrimSpace(s.cfg.Routing.Mode))
  }
  if s.coreManager != nil && (previousStrategy != nextStrategy || previousMode != nextMode) {
      var selector coreauth.Selector
      switch nextStrategy {
      case "fill-first":
          selector = &coreauth.FillFirstSelector{}
      default:
          selector = &coreauth.RoundRobinSelector{Mode: nextMode}
      }
      s.coreManager.SetSelector(selector)
      log.Infof("routing strategy updated to %s, mode: %s", nextStrategy, nextMode)
  }
  ```

  **Must NOT do**:
  - 핫 리로드 이외의 로직 변경

  **Parallelizable**: NO (Task 3 의존)

  **References**:
  
  **Pattern References**:
  - `sdk/cliproxy/service.go:529-550` - 현재 핫 리로드 코드
  - `sdk/cliproxy/service.go:559-561` - SetOAuthModelMappings() 핫 리로드 패턴

  **Acceptance Criteria**:
  
  - [ ] 빌드 성공: `go build ./...`
  - [ ] 기존 테스트 통과: `go test ./sdk/cliproxy/...`
  - [ ] config 파일에서 `routing.mode` 변경 시 selector가 재생성됨 (로그 확인)

  **Commit**: YES
  - Message: `feat(service): support routing.mode hot reload`
  - Files: `sdk/cliproxy/service.go`
  - Pre-commit: `go build ./... && go test ./sdk/cliproxy/...`

---

- [x] 5. Document in config.example.yaml

  **What to do**:
  - `config.example.yaml`의 `routing:` 섹션에 `mode` 필드 추가
  - 주석으로 설명

  **구체적 코드 변경**:
  ```yaml
  # config.example.yaml:78-81
  # 변경 전:
  routing:
    strategy: "round-robin" # round-robin (default), fill-first
  
  # 변경 후:
  routing:
    strategy: "round-robin" # round-robin (default), fill-first
    # mode: "key-based" # (optional) key-based: ignore provider, round-robin by model only
  ```

  **Must NOT do**:
  - 다른 설정 섹션 변경

  **Parallelizable**: NO (Task 4 의존)

  **References**:
  
  **Pattern References**:
  - `config.example.yaml:78-80` - 현재 routing 섹션

  **Acceptance Criteria**:
  
  - [ ] `routing.mode` 필드가 주석으로 문서화됨
  - [ ] 주석에 사용법 설명 포함
  - [ ] YAML 문법 오류 없음: `go run ./cmd/server -c config.example.yaml` 또는 수동 검증

  **Commit**: YES
  - Message: `docs(config): document routing.mode setting`
  - Files: `config.example.yaml`
  - Pre-commit: N/A

---

## Commit Strategy

| After Task | Message | Files | Verification |
|------------|---------|-------|--------------|
| 1 | `feat(config): add routing.mode field` | config.go, routing_config_test.go | `go test ./internal/config/...` |
| 2 | `feat(selector): add Mode field` | selector.go, selector_test.go | `go test ./sdk/cliproxy/auth/...` |
| 3 | `feat(builder): wire routing.mode` | builder.go | `go build ./...` |
| 4 | `feat(service): support mode hot reload` | service.go | `go test ./sdk/cliproxy/...` |
| 5 | `docs(config): document routing.mode` | config.example.yaml | manual |

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
- [x] `routing.mode: key-based` 설정 시 provider 무시 round-robin
- [x] 기존 동작 (mode 미설정) 변경 없음
- [x] 핫 리로드 시 mode 변경 반영
- [x] 모든 테스트 통과
- [x] config.example.yaml 문서화 완료
