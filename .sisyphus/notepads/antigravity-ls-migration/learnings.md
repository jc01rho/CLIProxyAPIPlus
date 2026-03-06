

## 2026-02-20 (Reactive Stream Subscription)

### 구현 개요
- 파일: `CLIProxyAPIPlus/internal/runtime/executor/ls_reactive_stream.go`

### 문제 배경
- LS가 SendUserCascadeMessage 후 "signal: killed"로 크래시됨
- 원인: LS는 cascade 생성 후 reactive updates 스트림 소비자를 기대함
- 소비자 없으면 LS write buffer가 차서 backpressure 발생 → 프로세스 킬됨

### 해결책: StreamCascadeReactiveUpdates 구독
ZeroGravity 참조 구현(`backend.rs` lines 418-527)을 Go로 포팅:

**Connect Protocol Frame Format:**
```
[1 byte flags] [4 bytes big-endian length] [payload]
- flags=0x00: data frame (JSON payload)
- flags=0x02: end frame (stream complete)
```

**핵심 구현 패턴:**

1. **HTTP 클라이언트 설정 (스트리밍용)**
   - `http.Client.Timeout = 0` (무제한 - 스트리밍 연결용)
   - `ResponseHeaderTimeout = 300s` (ls_client.go 패턴과 동일)
   - `DialContext.Timeout = 10s`

2. **Connect Protocol 요청**
   - URL: `https://127.0.0.1:{port}/exa.language_server_pb.LanguageServerService/StreamCascadeReactiveUpdates`
   - Content-Type: `application/connect+json`
   - Headers: `Connect-Protocol-Version: 1`, `x-codeium-csrf-token`
   - Body envelope: `[flags:1][length:4][payload]`

3. **백그라운드 고루틴 (핵심)**
   - `readStream()`이 별도 고루틴에서 지속적으로 chunk 소비
   - 버퍼에 데이터 축적 후 `processBuffer()`로 frame 단위 파싱
   - `processFrame()`에서 flags별 처리 (0x00=data, 0x02=end)
   - 스트림 소비이 목적 - 실제 데이터는 로깅만 함

4. **Integration with Executor**
   - `createCascadeWithReactiveSubscription()` 헬퍼 추가
   - 모든 Execute 메서드에서 cascade 생성 직후 구독 시작
   - `defer subscription.Close()`로 cascade 종료 시 정리

### 수정된 파일
1. `ls_reactive_stream.go` (새 파일, 261 lines)
2. `antigravity_executor.go`:
   - `createCascadeWithReactiveSubscription()` 메서드 추가
   - `Execute()`: cascade 생성 부분 수정
   - `executeClaudeNonStream()`: cascade 생성 부분 수정  
   - `ExecuteStream()`: cascade 생성 부분 수정

### 검증
```bash
cd CLIProxyAPIPlus
go build -o cliproxy ./cmd/server
# 빌드 성공
```

### 핵심 학습
- LS 바이너리는 단순 HTTP 요청-응답이 아닌, 연결 유지형 스트리밍 패턴 요구
- Reactive updates 스트림 소비는 필수이며 선택사항이 아님
- Connect Protocol envelope 파싱: `[flags:1][length:4][payload]`
- Background goroutine으로 지속적 소비 - 메인 플로우 차단 없음

