# Antigravity 쿼타/사용량 탐지 로직

> **문서 버전**: 1.0  
> **최종 업데이트**: 2026-01-19  
> **대상**: Quotio 개발자

---

## 1. 개요

이 문서는 Quotio 앱에서 Antigravity AI 제공자의 **쿼타(잔여량) 및 사용량**을 탐지하고 표시하는 로직을 설명합니다.

### 이 문서의 범위

| 포함 | 미포함 |
|------|--------|
| 잔여 쿼타 백분율 계산 | 구독 티어 탐지 (별도 문서 참조) |
| 리셋 시간 처리 | OAuth 인증 플로우 |
| 모델 그룹화 로직 | 계정 전환 로직 |
| UI 색상 코딩 | |
| 데이터 플로우 | |

> **참고**: 구독 티어(Free/Pro/Ultra) 탐지 로직은 [`antigravity-tier-detection.md`](./antigravity-tier-detection.md)를 참조하세요.

### 핵심 구성 요소

| 구성 요소 | 파일 위치 | 역할 |
|-----------|----------|------|
| `ModelQuota` | `Services/Antigravity/AntigravityQuotaFetcher.swift:114-235` | 개별 모델 쿼타 데이터 |
| `ProviderQuotaData` | `Services/Antigravity/AntigravityQuotaFetcher.swift:237-308` | 제공자별 쿼타 컨테이너 |
| `AntigravityModelGroup` | `Services/Antigravity/AntigravityQuotaFetcher.swift:12-47` | 모델 그룹화 열거형 |
| `AntigravityQuotaFetcher` | `Services/Antigravity/AntigravityQuotaFetcher.swift:437+` | API 호출 및 데이터 페칭 |
| `QuotaViewModel` | `ViewModels/QuotaViewModel.swift:1133-1159` | 쿼타 갱신 및 상태 관리 |
| `QuotaScreen` | `Views/Screens/QuotaScreen.swift:469-510` | UI 그룹화 및 표시 |
| `MenuBarSettingsManager` | `Models/MenuBarSettings.swift:360-370` | 집계 모드 설정 |

---

## 2. API 상세

### 2.1 쿼타 조회 엔드포인트

```
POST https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels
```

**헤더:**
```http
Authorization: Bearer {access_token}
User-Agent: antigravity/1.11.3 Darwin/arm64
Content-Type: application/json
```

**요청 본문:**
```json
{
    "project": "optional-project-id"
}
```

> `project`는 `loadCodeAssist` API에서 얻은 `cloudaicompanionProject` 값입니다.

### 2.2 API 응답 모델

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 371-382

nonisolated private struct QuotaAPIResponse: Codable, Sendable {
    let models: [String: ModelInfo]
}

nonisolated private struct ModelInfo: Codable, Sendable {
    let quotaInfo: QuotaInfo?
}

nonisolated private struct QuotaInfo: Codable, Sendable {
    let remainingFraction: Double?  // 0.0 ~ 1.0 (API가 분수로 반환)
    let resetTime: String?          // ISO8601 형식
}
```

### 2.3 응답 예시

```json
{
    "models": {
        "gemini-3-pro-high": {
            "quotaInfo": {
                "remainingFraction": 0.65,
                "resetTime": "2026-01-20T00:00:00Z"
            }
        },
        "claude-sonnet-4-5": {
            "quotaInfo": {
                "remainingFraction": 0.25,
                "resetTime": "2026-01-19T12:00:00Z"
            }
        }
    }
}
```

### 2.4 HTTP 상태 코드 처리

| 상태 코드 | 처리 |
|-----------|------|
| 200-299 | 정상 응답 파싱 |
| 403 | `isForbidden = true`로 설정 (쿼타 초과/접근 거부) |
| 기타 | `QuotaFetchError.httpError(code)` 예외 발생 |

### 2.5 재시도 로직

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 511-549

for attempt in 1...3 {
    do {
        // API 호출 시도
        ...
    } catch {
        lastError = error
        if attempt < 3 {
            try? await Task.sleep(nanoseconds: 1_000_000_000)  // 1초 대기
        }
    }
}
```

---

## 3. 데이터 구조

### 3.1 ModelQuota 구조체

개별 모델의 쿼타 정보를 저장합니다.

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 114-235

nonisolated struct ModelQuota: Codable, Identifiable, Sendable {
    let name: String               // 예: "gemini-3-pro-high"
    let percentage: Double         // 잔여 쿼타 0-100%
    let resetTime: String          // ISO8601 형식
    
    // 일부 제공자용 (Cursor 등)
    var used: Int?
    var limit: Int?
    var remaining: Int?
    
    var id: String { name }
    
    // 사용된 백분율 (100 - 잔여량)
    var usedPercentage: Double { 100 - percentage }
    
    // 포맷된 백분율 문자열
    var formattedPercentage: String { ... }
    
    // 사람이 읽을 수 있는 리셋 시간
    var formattedResetTime: String { ... }
    
    // UI용 표시명
    var displayName: String { ... }
    
    // 모델 그룹 (Claude, Gemini Pro, Gemini Flash)
    var modelGroup: AntigravityModelGroup? { ... }
}
```

### 3.2 ProviderQuotaData 구조체

제공자별 전체 쿼타 데이터를 저장합니다.

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 237-308

nonisolated struct ProviderQuotaData: Codable, Sendable {
    var models: [ModelQuota]       // 모델별 쿼타 목록
    var lastUpdated: Date          // 마지막 갱신 시간
    var isForbidden: Bool          // 403 응답 여부
    var planType: String?          // 플랜 타입
    var tokenExpiresAt: Date?      // Kiro용 토큰 만료 시간
    
    // Antigravity 모델 그룹화
    var groupedModels: [GroupedModelQuota] { ... }
    var hasGroupedModels: Bool { ... }
}
```

### 3.3 AntigravityModelGroup 열거형

모델을 논리적 그룹으로 분류합니다.

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 12-47

nonisolated enum AntigravityModelGroup: String, CaseIterable {
    case claude = "Claude"
    case geminiPro = "Gemini Pro"
    case geminiFlash = "Gemini Flash"
    
    static func group(for modelName: String) -> AntigravityModelGroup? {
        let name = modelName.lowercased()
        
        // Claude 그룹: gpt, oss 모델도 포함
        if name.contains("claude") || name.contains("gpt") || name.contains("oss") {
            return .claude
        }
        
        if name.contains("gemini") && name.contains("pro") {
            return .geminiPro
        }
        
        if name.contains("gemini") && name.contains("flash") {
            return .geminiFlash
        }
        
        return nil
    }
}
```

---

## 4. 잔여량 계산 로직

### 4.1 API 응답 변환

API는 `remainingFraction`을 0.0~1.0 범위의 분수로 반환합니다. 이를 0~100% 백분율로 변환합니다.

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 536-538

let percentage = (quotaInfo.remainingFraction ?? 0) * 100  // 0.0-1.0 → 0-100%
let resetTime = quotaInfo.resetTime ?? ""
models.append(ModelQuota(name: name, percentage: percentage, resetTime: resetTime))
```

### 4.2 백분율 의미

| `percentage` 값 | 의미 |
|-----------------|------|
| 100 | 쿼타 완전히 남음 (사용량 0%) |
| 50 | 쿼타 50% 남음 (50% 사용) |
| 0 | 쿼타 소진 (100% 사용) |
| < 0 | 쿼타 정보 없음/불명 |

### 4.3 사용량 계산

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 126-128

var usedPercentage: Double {
    100 - percentage  // 잔여량에서 사용량 계산
}
```

### 4.4 모델 필터링

API 응답에서 Gemini와 Claude 모델만 필터링합니다.

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 532-533

for (name, info) in quotaResponse.models {
    guard name.contains("gemini") || name.contains("claude") else { continue }
    // ...
}
```

---

## 5. 리셋 시간 처리

### 5.1 ISO8601 파싱

리셋 시간은 ISO8601 형식으로 제공됩니다.

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 203-234

var formattedResetTime: String {
    guard let date = ISO8601DateFormatter().date(from: resetTime) else {
        return "—"
    }
    
    let now = Date()
    let interval = date.timeIntervalSince(now)
    
    if interval <= 0 {
        return "now"  // 이미 지난 시간
    }
    
    let totalMinutes = Int(interval / 60)
    let hours = totalMinutes / 60
    let minutes = totalMinutes % 60
    let days = hours / 24
    let remainingHours = hours % 24
    
    // 사람이 읽기 쉬운 형식으로 변환
    if days > 0 {
        if remainingHours > 0 {
            return "\(days)d \(remainingHours)h"  // "3d 5h"
        }
        return "\(days)d"  // "3d"
    } else if hours > 0 {
        if minutes > 0 {
            return "\(hours)h \(minutes)m"  // "2h 30m"
        }
        return "\(hours)h"  // "2h"
    } else {
        return "\(max(1, minutes))m"  // "45m" (최소 1분)
    }
}
```

### 5.2 표시 형식 예시

| 남은 시간 | 표시 |
|-----------|------|
| 3일 5시간 | `3d 5h` |
| 2시간 30분 | `2h 30m` |
| 45분 | `45m` |
| 0분 이하 | `now` |
| 파싱 실패 | `—` |

---

## 6. 모델 그룹화

### 6.1 UI 표시용 4개 그룹

QuotaScreen에서는 Antigravity 모델을 4개 그룹으로 표시합니다.

```swift
// 파일: Views/Screens/QuotaScreen.swift
// 라인: 469-510

private var antigravityDisplayGroups: [AntigravityDisplayGroup] {
    guard let data = account.quotaData, provider == .antigravity else { return [] }
    
    var groups: [AntigravityDisplayGroup] = []
    
    // 1. Gemini 3 Pro: "gemini-3-pro" 포함, "image" 미포함
    let gemini3ProModels = data.models.filter { 
        $0.name.contains("gemini-3-pro") && !$0.name.contains("image") 
    }
    if !gemini3ProModels.isEmpty {
        let aggregatedQuota = settings.aggregateModelPercentages(gemini3ProModels.map(\.percentage))
        if aggregatedQuota >= 0 {
            groups.append(AntigravityDisplayGroup(
                name: "Gemini 3 Pro", 
                percentage: aggregatedQuota, 
                models: gemini3ProModels
            ))
        }
    }
    
    // 2. Gemini 3 Flash: "gemini-3-flash" 포함
    let gemini3FlashModels = data.models.filter { $0.name.contains("gemini-3-flash") }
    
    // 3. Gemini 3 Image: "image" 포함
    let geminiImageModels = data.models.filter { $0.name.contains("image") }
    
    // 4. Claude: "claude" 포함
    let claudeModels = data.models.filter { $0.name.contains("claude") }
    
    return groups.sorted { $0.percentage < $1.percentage }  // 낮은 쿼타 우선
}
```

### 6.2 그룹 필터링 조건

| 그룹 | 필터 조건 |
|------|-----------|
| **Gemini 3 Pro** | `name.contains("gemini-3-pro") && !name.contains("image")` |
| **Gemini 3 Flash** | `name.contains("gemini-3-flash")` |
| **Gemini 3 Image** | `name.contains("image")` |
| **Claude** | `name.contains("claude")` |

### 6.3 집계 모드

그룹 내 여러 모델의 쿼타를 하나로 집계합니다.

```swift
// 파일: Models/MenuBarSettings.swift
// 라인: 360-370

func aggregateModelPercentages(_ percentages: [Double]) -> Double {
    let validPercentages = percentages.filter { $0 >= 0 }
    guard !validPercentages.isEmpty else { return -1 }
    
    switch modelAggregationMode {
    case .lowest:
        return validPercentages.min() ?? -1   // 가장 낮은 값 사용
    case .average:
        return validPercentages.reduce(0, +) / Double(validPercentages.count)  // 평균
    }
}
```

| 집계 모드 | 설명 | 사용 케이스 |
|-----------|------|-------------|
| `.lowest` (기본값) | 그룹 내 가장 낮은 쿼타 | 보수적 표시, 병목 지점 강조 |
| `.average` | 그룹 내 평균 쿼타 | 전체적인 사용량 파악 |

---

## 7. UI 표시

### 7.1 색상 코딩

쿼타 상태에 따라 색상을 결정합니다.

```swift
// 파일: Views/Screens/QuotaScreen.swift
// 라인: 216-230

func statusColor(remainingPercent: Double) -> Color {
    let clamped = max(0, min(100, remainingPercent))
    let usedPercent = 100 - clamped
    let checkValue = displayMode == .used ? usedPercent : clamped
    
    if displayMode == .used {
        // 사용량 기준
        if checkValue < 70 { return .green }   // < 70% 사용: 정상
        if checkValue < 90 { return .yellow }  // < 90% 사용: 주의
        return .red                             // >= 90% 사용: 위험
    }
    
    // 잔여량 기준
    if checkValue > 50 { return .green }  // > 50% 남음: 정상
    if checkValue > 20 { return .orange } // > 20% 남음: 주의
    return .red                            // <= 20% 남음: 위험
}
```

### 7.2 색상 임계값 표

#### 잔여량 모드 (`remaining`)

| 잔여량 | 색상 | 상태 |
|--------|------|------|
| > 50% | 🟢 Green | 정상 |
| 20% ~ 50% | 🟠 Orange | 주의 |
| <= 20% | 🔴 Red | 위험 |

#### 사용량 모드 (`used`)

| 사용량 | 색상 | 상태 |
|--------|------|------|
| < 70% | 🟢 Green | 정상 |
| 70% ~ 90% | 🟡 Yellow | 주의 |
| >= 90% | 🔴 Red | 위험 |

### 7.3 표시 모드

```swift
// 파일: Models/MenuBarSettings.swift
// 라인: 151-178

enum QuotaDisplayMode: String, Codable, CaseIterable {
    case used = "used"           // "75% used" 형식
    case remaining = "remaining" // "25% left" 형식
    
    func displayValue(from remainingPercent: Double) -> Double {
        switch self {
        case .used: return 100 - remainingPercent
        case .remaining: return remainingPercent
        }
    }
}
```

### 7.4 표시 스타일

```swift
// 파일: Models/MenuBarSettings.swift
// 라인: 183-205

enum QuotaDisplayStyle: String, Codable, CaseIterable {
    case card = "card"           // 카드형 + 프로그레스 바
    case lowestBar = "lowestBar" // 최저 쿼타 강조 바
    case ring = "ring"           // 원형 프로그레스
}
```

| 스타일 | 설명 |
|--------|------|
| `card` | 모델별 개별 프로그레스 바가 있는 카드 |
| `lowestBar` | 가장 낮은 쿼타를 히어로 바로 강조, 나머지는 텍스트 |
| `ring` | 원형 프로그레스 링 그리드 |

---

## 8. 데이터 플로우

### 8.1 전체 플로우 다이어그램

```
┌─────────────────────────────────────────────────────────────────────┐
│                           인증 파일 저장소                             │
│              ~/.cli-proxy-api/antigravity-{email}.json              │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     AntigravityQuotaFetcher (Actor)                 │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  1. 인증 파일 읽기                                            │    │
│  │  2. 토큰 만료 시 갱신 (refreshAccessToken)                    │    │
│  │  3. fetchQuota() API 호출                                    │    │
│  │  4. remainingFraction * 100 → percentage 변환               │    │
│  │  5. ProviderQuotaData 반환                                   │    │
│  └─────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     QuotaViewModel (@MainActor)                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  providerQuotas[.antigravity] = quotas                       │    │
│  │  subscriptionInfos[email] = info                            │    │
│  └─────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│                         QuotaScreen (View)                          │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  viewModel.providerQuotas[provider] 읽기                     │    │
│  │  antigravityDisplayGroups 생성                               │    │
│  │  색상 코딩 및 표시                                            │    │
│  └─────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
```

### 8.2 코드 플로우

#### Step 1: QuotaViewModel에서 갱신 시작

```swift
// 파일: ViewModels/QuotaViewModel.swift
// 라인: 1133-1146

private func refreshAntigravityQuotasInternal() async {
    // 쿼타와 구독 정보를 한 번에 가져옴 (중복 API 호출 방지)
    let (quotas, subscriptions) = await antigravityFetcher.fetchAllAntigravityData()
    
    providerQuotas[.antigravity] = quotas
    
    // 기존 데이터에 병합 (API 실패 시 데이터 보존)
    for (email, info) in subscriptions {
        subscriptionInfos[email] = info
    }
    
    // IDE에서 활성 계정 탐지
    await antigravitySwitcher.detectActiveAccount()
}
```

#### Step 2: AntigravityQuotaFetcher에서 데이터 페칭

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 745-789

func fetchAllAntigravityData(authDir: String = "~/.cli-proxy-api") async 
    -> (quotas: [String: ProviderQuotaData], subscriptions: [String: SubscriptionInfo]) {
    
    // 캐시 초기화
    clearCache()
    
    let expandedPath = NSString(string: authDir).expandingTildeInPath
    
    // 모든 antigravity-*.json 파일 병렬 처리
    await withTaskGroup(of: (String, ProviderQuotaData?, SubscriptionInfo?).self) { group in
        for file in files where file.hasPrefix("antigravity-") && file.hasSuffix(".json") {
            group.addTask {
                let result = await self.fetchQuotaAndSubscriptionForAuthFile(at: filePath)
                return (email, result.quota, result.subscription)
            }
        }
        
        for await (email, quota, subscription) in group {
            if let quota = quota {
                quotaResults[email] = quota
            }
            if let subscription = subscription {
                subscriptionResults[email] = subscription
            }
        }
    }
    
    return (quotaResults, subscriptionResults)
}
```

#### Step 3: API 호출 및 변환

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 493-553

func fetchQuota(accessToken: String) async throws -> ProviderQuotaData {
    // 1. projectId 먼저 가져옴
    let projectId = await fetchProjectId(accessToken: accessToken)
    
    // 2. API 요청 생성
    var request = URLRequest(url: URL(string: quotaAPIURL)!)
    request.httpMethod = "POST"
    request.addValue("Bearer \(accessToken)", forHTTPHeaderField: "Authorization")
    
    // 3. API 호출 (최대 3회 재시도)
    let (data, response) = try await session.data(for: request)
    
    // 4. 응답 파싱 및 변환
    let quotaResponse = try decoder.decode(QuotaAPIResponse.self, from: data)
    
    var models: [ModelQuota] = []
    for (name, info) in quotaResponse.models {
        guard name.contains("gemini") || name.contains("claude") else { continue }
        
        if let quotaInfo = info.quotaInfo {
            // 핵심: 0.0-1.0 → 0-100% 변환
            let percentage = (quotaInfo.remainingFraction ?? 0) * 100
            let resetTime = quotaInfo.resetTime ?? ""
            models.append(ModelQuota(name: name, percentage: percentage, resetTime: resetTime))
        }
    }
    
    return ProviderQuotaData(models: models, lastUpdated: Date())
}
```

---

## 9. 지원 모델 목록

### 9.1 Antigravity Gemini 모델

| API 모델명 | 표시명 | 그룹 |
|------------|--------|------|
| `gemini-3-pro-high` | Gemini 3 Pro | Gemini Pro |
| `gemini-3-pro` | Gemini 3 Pro | Gemini Pro |
| `gemini-3-flash` | Gemini 3 Flash | Gemini Flash |
| `gemini-3-flash-high` | Gemini 3 Flash | Gemini Flash |
| `gemini-3-pro-image` | Gemini 3 Image | (Image) |
| `gemini-3-flash-image` | Gemini 3 Image | (Image) |

### 9.2 Antigravity Claude 모델

| API 모델명 | 표시명 | 그룹 |
|------------|--------|------|
| `claude-sonnet-4-5` | Claude Sonnet 4.5 | Claude |
| `claude-sonnet-4-5-thinking` | Claude Sonnet 4.5 (Thinking) | Claude |
| `claude-opus-4` | Claude Opus 4 | Claude |
| `claude-opus-4-5` | Claude Opus 4.5 | Claude |
| `claude-opus-4-5-thinking` | Claude Opus 4.5 (Thinking) | Claude |
| `claude-4-sonnet` | Claude 4 Sonnet | Claude |
| `claude-4-opus` | Claude 4 Opus | Claude |

### 9.3 표시명 매핑 코드

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 153-201

var displayName: String {
    switch name {
    // Antigravity Gemini models
    case "gemini-3-pro-high": return "Gemini 3 Pro"
    case "gemini-3-pro": return "Gemini 3 Pro"
    case "gemini-3-flash": return "Gemini 3 Flash"
    case "gemini-3-flash-high": return "Gemini 3 Flash"
    case "gemini-3-pro-image": return "Gemini 3 Image"
    case "gemini-3-flash-image": return "Gemini 3 Image"
    // Antigravity Claude models
    case "claude-sonnet-4-5": return "Claude Sonnet 4.5"
    case "claude-sonnet-4-5-thinking": return "Claude Sonnet 4.5 (Thinking)"
    case "claude-opus-4": return "Claude Opus 4"
    case "claude-opus-4-5": return "Claude Opus 4.5"
    case "claude-opus-4-5-thinking": return "Claude Opus 4.5 (Thinking)"
    // ... 기타 제공자 모델들
    default: return name
    }
}
```

---

## 10. 코드 참조 요약

### 10.1 핵심 파일 및 라인 번호

| 기능 | 파일 | 라인 |
|------|------|------|
| API 엔드포인트 | `AntigravityQuotaFetcher.swift` | 438-439 |
| API 응답 모델 | `AntigravityQuotaFetcher.swift` | 371-382 |
| 백분율 변환 | `AntigravityQuotaFetcher.swift` | 536-538 |
| ModelQuota 구조체 | `AntigravityQuotaFetcher.swift` | 114-235 |
| ProviderQuotaData | `AntigravityQuotaFetcher.swift` | 237-308 |
| AntigravityModelGroup | `AntigravityQuotaFetcher.swift` | 12-47 |
| fetchQuota 메서드 | `AntigravityQuotaFetcher.swift` | 493-553 |
| 리셋 시간 포맷 | `AntigravityQuotaFetcher.swift` | 203-234 |
| ViewModel 갱신 | `QuotaViewModel.swift` | 1133-1146 |
| UI 그룹화 | `QuotaScreen.swift` | 469-510 |
| 색상 코딩 | `QuotaScreen.swift` | 216-230 |
| 집계 모드 | `MenuBarSettings.swift` | 360-370 |
| 표시 모드 | `MenuBarSettings.swift` | 151-178 |

### 10.2 관련 타입 의존성

```
QuotaViewModel
├── AntigravityQuotaFetcher (actor)
│   ├── AntigravityAuthFile
│   ├── QuotaAPIResponse
│   ├── ModelInfo
│   └── QuotaInfo
├── providerQuotas: [AIProvider: [String: ProviderQuotaData]]
│   └── ProviderQuotaData
│       ├── models: [ModelQuota]
│       │   └── AntigravityModelGroup
│       └── groupedModels: [GroupedModelQuota]
└── subscriptionInfos: [String: SubscriptionInfo]

QuotaScreen
├── QuotaDisplayHelper
├── MenuBarSettingsManager
│   ├── QuotaDisplayMode
│   ├── QuotaDisplayStyle
│   └── ModelAggregationMode
└── AntigravityDisplayGroup
```

---

## 11. 오류 처리

### 11.1 QuotaFetchError 열거형

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 825-843

nonisolated enum QuotaFetchError: LocalizedError {
    case invalidURL
    case invalidResponse
    case forbidden         // 403 응답
    case httpError(Int)    // 기타 HTTP 오류
    case unknown
    case apiErrorMessage(String)
    
    var errorDescription: String? {
        switch self {
        case .invalidURL: return "Invalid URL"
        case .invalidResponse: return "Invalid response from server"
        case .forbidden: return "Access forbidden"
        case .httpError(let code): return "HTTP error: \(code)"
        case .unknown: return "Unknown error"
        case .apiErrorMessage(let msg): return "API error: \(msg)"
        }
    }
}
```

### 11.2 403 응답 처리

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 519-521

if httpResponse.statusCode == 403 {
    return ProviderQuotaData(isForbidden: true)  // UI에서 경고 표시
}
```

---

## 12. 참고 사항

### 12.1 토큰 갱신

인증 토큰이 만료되면 자동으로 갱신합니다.

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 467-491

func refreshAccessToken(refreshToken: String) async throws -> String {
    // Google OAuth2 토큰 갱신 API 호출
    let params = [
        "client_id": clientId,
        "client_secret": clientSecret,
        "refresh_token": refreshToken,
        "grant_type": "refresh_token"
    ]
    // ...
}
```

### 12.2 캐시 관리

각 갱신 사이클 시작 시 구독 캐시를 초기화하여 메모리를 확보합니다.

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 462-465

func clearCache() {
    // 기존 용량을 해제하기 위해 새 딕셔너리 생성
    subscriptionCache = [:]
}
```

### 12.3 병렬 처리

여러 계정의 쿼타를 병렬로 가져와 성능을 최적화합니다.

```swift
await withTaskGroup(of: ...) { group in
    for file in files {
        group.addTask {
            await self.fetchQuotaAndSubscriptionForAuthFile(at: filePath)
        }
    }
}
```

---

## 변경 이력

| 버전 | 날짜 | 변경 내용 |
|------|------|-----------|
| 1.0 | 2026-01-19 | 최초 작성 |
