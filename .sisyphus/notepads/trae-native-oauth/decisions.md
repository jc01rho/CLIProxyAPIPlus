- Followed the function signature in the 'MUST DO' section: GenerateNativeAuthURL(callbackURL, appVersion) as it correctly encapsulates the ID generation logic required by the task.

- Import order는 `storage.json` 우선, `auth.json` fallback으로 결정했다. 최신 Trae 저장 포맷을 먼저 사용하면서 기존 설치/구버전 파일과의 호환성을 유지하기 위함이다.
