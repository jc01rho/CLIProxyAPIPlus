# Antigravity 구독 티어 탐지 로직

> **문서 버전**: 1.0  
> **최종 업데이트**: 2026-01-17  
> **대상**: Quotio 개발자

---

## 1. 개요

이 문서는 Quotio 앱에서 Antigravity AI 제공자의 구독 티어(Free/Standard, Pro, Ultra)를 탐지하고 표시하는 로직을 설명합니다.

Antigravity는 Google의 AI 코딩 어시스턴트 서비스로, 사용자는 여러 티어 중 하나를 구독할 수 있습니다. Quotio는 각 계정의 구독 정보를 API로 조회하여 UI에 적절한 배지와 색상으로 표시합니다.

### 핵심 구성 요소

| 구성 요소 | 파일 위치 | 역할 |
|-----------|----------|------|
| `SubscriptionTier` | `Services/Antigravity/AntigravityQuotaFetcher.swift:312-322` | 개별 티어 데이터 모델 |
| `SubscriptionInfo` | `Services/Antigravity/AntigravityQuotaFetcher.swift:329-367` | 구독 정보 래퍼 + 티어 판별 로직 |
| `AntigravityQuotaFetcher` | `Services/Antigravity/AntigravityQuotaFetcher.swift:437+` | API 호출 및 데이터 페칭 |
| `QuotaViewModel` | `ViewModels/QuotaViewModel.swift:84` | 구독 정보 저장 및 관리 |
| `tierConfig` (UI) | 여러 View 파일 | 티어별 색상 및 표시명 결정 |

---

## 2. 티어 레벨

Quotio가 인식하는 Antigravity 구독 티어:

| 티어 | ID 패턴 | 표시명 | UI 색상 | 유료 여부 |
|------|---------|--------|---------|----------|
| **Ultra** | `ultra` 포함 | "Ultra" | Orange (🟠) | 유료 |
| **Pro** | `pro` 포함 | "Pro" | Blue/Purple (🔵/🟣) | 유료 |
| **Standard/Free** | `standard` 또는 `free` 포함 | "Free" | Secondary/Gray (⚫) | 무료 |
| **Unknown** | 패턴 매칭 실패 | API 원본 표시명 사용 | Secondary/Gray (⚫) | 알 수 없음 |

### 티어 우선순위

탐지 로직은 다음 순서로 티어를 확인합니다:

1. **Ultra** (최우선) - `tierId` 또는 `tierName`에 "ultra" 포함 시
2. **Pro** - `tierId` 또는 `tierName`에 "pro" 포함 시
3. **Standard/Free** - `tierId` 또는 `tierName`에 "standard" 또는 "free" 포함 시
4. **Fallback** - 위 조건에 해당 없을 시 API에서 받은 `tierDisplayName` 그대로 사용

---

## 3. 탐지 로직

### 3.1 효과 티어 결정 (Effective Tier)

API에서 두 가지 티어 정보를 받을 수 있습니다:
- `currentTier`: 현재 활성 티어
- `paidTier`: 유료 구독 티어 (있는 경우)

`SubscriptionInfo` 구조체는 `paidTier`를 `currentTier`보다 우선시합니다:

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 337-340

/// Get the effective tier - prioritize paidTier over currentTier
private var effectiveTier: SubscriptionTier? {
    paidTier ?? currentTier
}
```

### 3.2 티어 ID 및 표시명 접근

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 342-352

var tierDisplayName: String {
    effectiveTier?.name ?? "Unknown"
}

var tierId: String {
    effectiveTier?.id ?? "unknown"
}
```

### 3.3 유료 티어 판별

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 354-357

var isPaidTier: Bool {
    guard let id = effectiveTier?.id else { return false }
    return id.contains("pro") || id.contains("ultra")
}
```

---

## 4. API 상세

### 4.1 구독 정보 조회 엔드포인트

| 항목 | 값 |
|------|-----|
| **URL** | `https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist` |
| **Method** | `POST` |
| **Content-Type** | `application/json` |

### 4.2 요청 헤더

```http
Authorization: Bearer {access_token}
User-Agent: antigravity/1.11.3 Darwin/arm64
Content-Type: application/json
```

### 4.3 요청 본문

```json
{
  "metadata": {
    "ideType": "ANTIGRAVITY"
  }
}
```

### 4.4 응답 형식 (SubscriptionInfo)

```json
{
  "currentTier": {
    "id": "standard",
    "name": "Gemini Code Assist Standard",
    "description": "Free tier description",
    "privacyNotice": {
      "showNotice": true,
      "noticeText": "Privacy notice text"
    },
    "isDefault": true,
    "upgradeSubscriptionUri": "https://...",
    "upgradeSubscriptionText": "Upgrade to Pro",
    "upgradeSubscriptionType": "SUBSCRIPTION",
    "userDefinedCloudaicompanionProject": false
  },
  "allowedTiers": [...],
  "cloudaicompanionProject": "project-id",
  "gcpManaged": false,
  "upgradeSubscriptionUri": "https://...",
  "paidTier": null
}
```

### 4.5 API 호출 구현

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 567-591

func fetchSubscriptionInfo(accessToken: String) async -> SubscriptionInfo? {
    var request = URLRequest(url: URL(string: loadProjectAPIURL)!)
    request.httpMethod = "POST"
    request.addValue("Bearer \(accessToken)", forHTTPHeaderField: "Authorization")
    request.addValue(userAgent, forHTTPHeaderField: "User-Agent")
    request.addValue("application/json", forHTTPHeaderField: "Content-Type")
    
    let payload = ["metadata": ["ideType": "ANTIGRAVITY"]]
    request.httpBody = try? JSONSerialization.data(withJSONObject: payload)
    
    do {
        let (data, response) = try await session.data(for: request)
        
        guard let httpResponse = response as? HTTPURLResponse,
              200...299 ~= httpResponse.statusCode else {
            return nil
        }
        
        let subscriptionInfo = try JSONDecoder().decode(SubscriptionInfo.self, from: data)
        return subscriptionInfo
        
    } catch {
        return nil
    }
}
```

---

## 5. 데이터 구조

### 5.1 SubscriptionTier

개별 티어의 상세 정보를 담는 구조체:

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 312-322

nonisolated struct SubscriptionTier: Codable, Sendable {
    let id: String                              // 예: "ultra", "pro", "standard", "free"
    let name: String                            // 표시명 (예: "Gemini Code Assist Pro")
    let description: String                     // 티어 설명
    let privacyNotice: PrivacyNotice?          // 개인정보 알림 (선택)
    let isDefault: Bool?                        // 기본 티어 여부
    let upgradeSubscriptionUri: String?         // 업그레이드 URL
    let upgradeSubscriptionText: String?        // 업그레이드 버튼 텍스트
    let upgradeSubscriptionType: String?        // 업그레이드 유형
    let userDefinedCloudaicompanionProject: Bool? // 사용자 정의 프로젝트 여부
}
```

### 5.2 SubscriptionInfo

구독 전체 정보를 래핑하고 티어 판별 로직을 포함하는 구조체:

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 329-367

nonisolated struct SubscriptionInfo: Codable, Sendable {
    let currentTier: SubscriptionTier?          // 현재 티어
    let allowedTiers: [SubscriptionTier]?       // 허용된 티어 목록
    let cloudaicompanionProject: String?        // GCP 프로젝트 ID
    let gcpManaged: Bool?                       // GCP 관리 여부
    let upgradeSubscriptionUri: String?         // 업그레이드 URL
    let paidTier: SubscriptionTier?             // 유료 티어 (있는 경우)
    
    // 효과 티어 - paidTier 우선
    private var effectiveTier: SubscriptionTier? {
        paidTier ?? currentTier
    }
    
    var tierId: String {
        effectiveTier?.id ?? "unknown"
    }
    
    var tierDisplayName: String {
        effectiveTier?.name ?? "Unknown"
    }
    
    var isPaidTier: Bool {
        guard let id = effectiveTier?.id else { return false }
        return id.contains("pro") || id.contains("ultra")
    }
    
    var canUpgrade: Bool {
        effectiveTier?.upgradeSubscriptionUri != nil
    }
    
    var upgradeURL: URL? {
        guard let uri = effectiveTier?.upgradeSubscriptionUri else { return nil }
        return URL(string: uri)
    }
}
```

### 5.3 PrivacyNotice

개인정보 관련 알림 구조체:

```swift
// 파일: Services/Antigravity/AntigravityQuotaFetcher.swift
// 라인: 324-327

nonisolated struct PrivacyNotice: Codable, Sendable {
    let showNotice: Bool?
    let noticeText: String?
}
```

---

## 6. UI 표시 로직

티어 표시 로직은 앱 내 3곳에서 동일한 패턴으로 구현되어 있습니다.

### 6.1 StatusBarMenuBuilder (메뉴 바)

```swift
// 파일: Services/StatusBarMenuBuilder.swift
// 라인: 622-639

private var tierConfig: (name: String, bgColor: Color, textColor: Color)? {
    guard let info = subscriptionInfo else { return nil }
    
    let tierId = info.tierId.lowercased()
    let tierName = info.tierDisplayName.lowercased()
    
    if tierId.contains("ultra") || tierName.contains("ultra") {
        return ("Ultra", .orange.opacity(0.15), .orange)
    }
    if tierId.contains("pro") || tierName.contains("pro") {
        return ("Pro", .blue.opacity(0.15), .blue)
    }
    if tierId.contains("standard") || tierId.contains("free") ||
       tierName.contains("standard") || tierName.contains("free") {
        return ("Free", .secondary.opacity(0.1), .secondary)
    }
    return (info.tierDisplayName, .secondary.opacity(0.1), .secondary)
}
```

**색상 구성:**
| 티어 | 배경색 | 텍스트색 |
|------|--------|----------|
| Ultra | `orange.opacity(0.15)` | `orange` |
| Pro | `blue.opacity(0.15)` | `blue` |
| Free/Standard | `secondary.opacity(0.1)` | `secondary` |
| Fallback | `secondary.opacity(0.1)` | `secondary` |

### 6.2 QuotaScreen - SubscriptionBadgeV2

```swift
// 파일: Views/Screens/QuotaScreen.swift
// 라인: 954-976

private var tierConfig: (name: String, color: Color) {
    let tierId = info.tierId.lowercased()
    let tierName = info.tierDisplayName.lowercased()
    
    // Check for Ultra tier (highest priority)
    if tierId.contains("ultra") || tierName.contains("ultra") {
        return ("Ultra", .orange)
    }
    
    // Check for Pro tier
    if tierId.contains("pro") || tierName.contains("pro") {
        return ("Pro", .purple)
    }
    
    // Check for Free/Standard tier
    if tierId.contains("standard") || tierId.contains("free") || 
       tierName.contains("standard") || tierName.contains("free") {
        return ("Free", .secondary)
    }
    
    // Fallback: use the display name from API
    return (info.tierDisplayName, .secondary)
}
```

**배지 스타일:**
```swift
// 라인: 978-987
var body: some View {
    Text(tierConfig.name)
        .font(.caption2)
        .fontWeight(.medium)
        .foregroundStyle(tierConfig.color)
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .background(tierConfig.color.opacity(0.12))
        .clipShape(Capsule())
}
```

### 6.3 색상 차이점

동일한 탐지 로직이지만 표시 위치에 따라 약간의 색상 차이가 있습니다:

| 컴포넌트 | Pro 색상 |
|----------|----------|
| StatusBarMenuBuilder | `.blue` |
| SubscriptionBadgeV2 | `.purple` |

---

## 7. 데이터 플로우

### 7.1 전체 플로우 다이어그램

```
┌─────────────────────────────────────────────────────────────────────┐
│                         앱 시작 / 새로고침                            │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│  QuotaViewModel.refreshAntigravityQuotasInternal()                  │
│  파일: ViewModels/QuotaViewModel.swift:1133-1146                     │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│  AntigravityQuotaFetcher.fetchAllAntigravityData()                  │
│  파일: Services/Antigravity/AntigravityQuotaFetcher.swift:747+       │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                    ┌───────────────┴───────────────┐
                    ▼                               ▼
┌──────────────────────────────┐    ┌──────────────────────────────┐
│  인증 파일 스캔                │    │  (각 계정별 병렬 처리)         │
│  ~/.cli-proxy-api/           │    │                              │
│  antigravity-{email}.json    │    │                              │
└──────────────────────────────┘    └──────────────────────────────┘
                    │                               │
                    ▼                               ▼
┌──────────────────────────────┐    ┌──────────────────────────────┐
│  토큰 만료 확인                │    │  fetchSubscriptionInfo()     │
│  필요시 refreshAccessToken()  │    │  API 호출                     │
└──────────────────────────────┘    └──────────────────────────────┘
                    │                               │
                    └───────────────┬───────────────┘
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│  QuotaViewModel.subscriptionInfos[email] = info                     │
│  파일: ViewModels/QuotaViewModel.swift:84                            │
│  타입: [String: SubscriptionInfo]                                    │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                    ┌───────────────┴───────────────┐
                    ▼                               ▼
┌──────────────────────────────┐    ┌──────────────────────────────┐
│  StatusBarMenuBuilder        │    │  QuotaScreen                 │
│  tierConfig 계산              │    │  SubscriptionBadgeV2         │
│  메뉴 바 배지 표시             │    │  할당량 화면 배지 표시         │
└──────────────────────────────┘    └──────────────────────────────┘
```

### 7.2 인증 파일 위치

Antigravity 계정별 인증 정보는 다음 위치에 저장됩니다:

```
~/.cli-proxy-api/antigravity-{email}.json
```

파일명 예시:
- `antigravity-user_gmail.com.json`
- `antigravity-developer_company.com.json`

### 7.3 데이터 저장 및 접근

```swift
// 파일: ViewModels/QuotaViewModel.swift
// 라인: 83-84

/// Subscription info per account (email -> SubscriptionInfo)
var subscriptionInfos: [String: SubscriptionInfo] = [:]
```

**데이터 병합 전략:**
```swift
// 파일: ViewModels/QuotaViewModel.swift
// 라인: 1139-1142

// Merge instead of replace to preserve data if API fails
for (email, info) in subscriptions {
    subscriptionInfos[email] = info
}
```

API 호출 실패 시에도 기존 데이터를 유지하기 위해 교체(replace) 대신 병합(merge)합니다.

---

## 8. 코드 참조 요약

### 주요 파일

| 파일 경로 | 라인 범위 | 내용 |
|-----------|----------|------|
| `Services/Antigravity/AntigravityQuotaFetcher.swift` | 312-322 | `SubscriptionTier` 구조체 |
| `Services/Antigravity/AntigravityQuotaFetcher.swift` | 324-327 | `PrivacyNotice` 구조체 |
| `Services/Antigravity/AntigravityQuotaFetcher.swift` | 329-367 | `SubscriptionInfo` 구조체 |
| `Services/Antigravity/AntigravityQuotaFetcher.swift` | 437-453 | `AntigravityQuotaFetcher` 초기화 및 상수 |
| `Services/Antigravity/AntigravityQuotaFetcher.swift` | 567-591 | `fetchSubscriptionInfo()` API 호출 |
| `Services/Antigravity/AntigravityQuotaFetcher.swift` | 593-616 | `fetchSubscriptionInfoForAuthFile()` |
| `Services/Antigravity/AntigravityQuotaFetcher.swift` | 618-642 | `fetchAllSubscriptionInfo()` |
| `ViewModels/QuotaViewModel.swift` | 83-84 | `subscriptionInfos` 저장소 |
| `ViewModels/QuotaViewModel.swift` | 1133-1146 | `refreshAntigravityQuotasInternal()` |
| `Services/StatusBarMenuBuilder.swift` | 622-639 | 메뉴 바 `tierConfig` |
| `Views/Screens/QuotaScreen.swift` | 954-976 | 할당량 화면 `tierConfig` |

### API 상수

| 상수 | 값 | 위치 |
|------|-----|------|
| `loadProjectAPIURL` | `https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist` | 라인 439 |
| `userAgent` | `antigravity/1.11.3 Darwin/arm64` | 라인 443 |
| `clientId` | `1071006060591-...apps.googleusercontent.com` | 라인 441 |

---

## 9. 개발자 가이드

### 9.1 새 티어 추가하기

새로운 Antigravity 티어가 추가되면 다음 위치를 수정해야 합니다:

1. **UI 표시 로직** (3곳 모두):
   - `StatusBarMenuBuilder.swift` - `tierConfig` 계산 프로퍼티
   - `QuotaScreen.swift` - `SubscriptionBadgeV2.tierConfig`
   - (추가 위치가 있다면 해당 위치도)

2. **유료 티어 판별** (필요 시):
   - `AntigravityQuotaFetcher.swift` - `SubscriptionInfo.isPaidTier`

### 9.2 테스트 시나리오

티어 탐지 로직을 테스트할 때 확인해야 할 시나리오:

| 시나리오 | 예상 결과 |
|----------|----------|
| `tierId: "ultra"` | Ultra 배지 (Orange) |
| `tierId: "pro"` | Pro 배지 (Blue/Purple) |
| `tierId: "standard"` | Free 배지 (Gray) |
| `tierId: "free"` | Free 배지 (Gray) |
| `tierName: "Ultra Plan"`, `tierId: "custom"` | Ultra 배지 (이름 기반 매칭) |
| `tierId: null`, `tierName: null` | "Unknown" 표시 |
| `paidTier` 존재 + `currentTier` 존재 | `paidTier` 우선 적용 |

### 9.3 주의사항

- **대소문자 처리**: 티어 ID와 이름은 `lowercased()`로 변환 후 비교합니다
- **부분 문자열 매칭**: `contains()`를 사용하므로 "ultra_v2" 같은 ID도 Ultra로 인식됩니다
- **Fallback 동작**: 알 수 없는 티어는 API 원본 `tierDisplayName`을 그대로 표시합니다
- **캐싱**: `AntigravityQuotaFetcher`는 리프레시 사이클 중 구독 정보를 캐싱합니다 (라인 448)

---

## 10. 관련 문서

- [AGENTS.md](/AGENTS.md) - 프로젝트 전체 가이드라인
- [Services/AGENTS.md](/Quotio/Services/AGENTS.md) - 서비스 레이어 문서
- [Views/AGENTS.md](/Quotio/Views/AGENTS.md) - 뷰 레이어 문서
