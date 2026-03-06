## 2026-02-20

- SQLite 드라이버는 CGO-free 요구사항을 만족하기 위해 `modernc.org/sqlite`를 선택했다.
- `ReadVSCDBToken`는 DB 경로 미입력 시 `DefaultVSCDBPath()`를 사용하도록 하여 운영 환경 기본 동작을 보장했다.
- 토큰 마스킹은 요구사항(앞 4자리만 표시)에 맞춰 `xxxx...` 포맷 전용 헬퍼(`maskTokenPrefix`)를 사용했다.
