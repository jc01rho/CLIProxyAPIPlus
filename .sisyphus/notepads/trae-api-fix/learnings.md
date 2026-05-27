# Trae API Executor 수정 완료

## 주요 변경사항

### 1. 엔드포인트 변경
- **기존**: `https://api-sg-central.trae.ai/v1/chat/completions` (OpenAI 호환)
- **수정**: `https://trae-api-sg.mchost.guru/api/ide/v1/chat` (Trae 전용)

### 2. 헤더 설정 변경
**기존 (잘못됨)**:
```go
httpReq.Header.Set("Authorization", "Bearer "+accessToken)
httpReq.Header.Set("Content-Type", "application/json")
httpReq.Header.Set("Accept", "application/json")
```

**수정 (올바름)**:
```go
httpReq.Header.Set("Content-Type", "application/json")
httpReq.Header.Set("x-app-id", appID)
httpReq.Header.Set("x-ide-version", "1.2.10")
httpReq.Header.Set("x-ide-version-code", "20250325")
httpReq.Header.Set("x-ide-version-type", "stable")
httpReq.Header.Set("x-device-cpu", "AMD")
httpReq.Header.Set("x-device-id", deviceID)
httpReq.Header.Set("x-machine-id", machineID)
httpReq.Header.Set("x-device-brand", deviceBrand)
httpReq.Header.Set("x-device-type", "windows")
httpReq.Header.Set("x-ide-token", accessToken)  // Authorization 대신!
httpReq.Header.Set("accept", "*/*")
httpReq.Header.Set("Connection", "keep-alive")
```

### 3. 요청 형식 변환 (OpenAI → Trae)
- OpenAI `messages` 배열 → Trae `user_input` + `chat_history`
- 모델명 매핑 추가 (claude-3-7-sonnet → aws_sdk_claude37_sonnet)
- 세션 ID 생성 및 관리
- 디바이스 정보 동적 생성

### 4. 응답 파싱 변경
**Trae SSE 형식**:
```
event: output
data: {"response": "Hello", "reasoning_content": "", "finish_reason": ""}

event: done
data: {"finish_reason": "stop"}
```

**OpenAI 형식으로 변환**:
```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion.chunk",
  "created": 1234567890,
  "model": "claude-3-7-sonnet",
  "choices": [{
    "index": 0,
    "delta": {"content": "Hello"},
    "finish_reason": null
  }]
}
```

## 핵심 함수들

### convertOpenAIToTrae()
- OpenAI 요청을 Trae API 형식으로 변환
- 세션 ID 생성, 컨텍스트 리졸버 설정
- 변수 JSON 구성

### generateDeviceInfo()
- 동적 디바이스 정보 생성
- device_id, machine_id, device_brand 랜덤 생성

### convertModelName()
- 모델명 매핑 (OpenAI 호환명 → Trae 내부명)

## 빌드 결과
✅ `go build ./...` 성공