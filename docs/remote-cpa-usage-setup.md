# 원격 CPA Usage Export 설정

이 문서는 원격 CLIProxyAPIPlus(CPA) 인스턴스를
`keeper-export/v1` push 프로토콜로 CPA Usage Keeper에 연결하는 방법을 설명합니다.

각 CPA 송신기마다 Keeper CPA instance 하나와 export credential 하나를
사용해야 합니다. 서로 다른 CPA 사이에서 credential을 공유하지 마세요.

## 1. Keeper에서 CPA instance 생성

CPA Usage Keeper에서 다음 메뉴를 엽니다.

```text
Settings > CPA Instances > Create CPA Instance
```

식별하기 쉬운 CPA 이름과 export credential 이름을 입력합니다. 최초 credential에는
다음 세 scope를 모두 유지하세요.

```text
identity:test
usage:push
metadata:push
```

표시되는 Bearer token은 즉시 복사합니다. Keeper는 token을 한 번만 보여 주며,
나중에 복구할 수 없습니다.

## 2. 원격 CPA에 token 저장

`token-env`(권장) 또는 직접 `token` 문자열을 설정하는 방식 중 하나를 선택합니다.
두 방식을 동시에 설정하거나, push 모드에서 둘 다 비워 두면 CPA가 설정을
거부합니다.

### 권장: 환경변수 사용

systemd 설치에서는 root 전용 환경 파일을 만듭니다.

```bash
sudo install -d -m 0700 -o root -g root /etc/cli-proxy-api
sudo install -m 0600 -o root -g root /dev/null /etc/cli-proxy-api/keeper-export.env
sudoedit /etc/cli-proxy-api/keeper-export.env
```

파일에 token을 추가합니다.

```env
CPA_KEEPER_INGEST_TOKEN=<Keeper에서-한-번만-표시된-Bearer-token>
```

CPA systemd service에서 이 파일을 참조하도록 설정합니다.

```ini
[Service]
EnvironmentFile=/etc/cli-proxy-api/keeper-export.env
```

명령행 인수로 token을 전달하지 마세요. 같은 시스템의 다른 사용자가 명령행을
조회할 수 있습니다.

### 대안: config.yaml에 직접 token 문자열 입력

systemd 환경변수를 운영하기 어렵다면, CPA 프로세스 소유자만 읽을 수 있도록
제한된 `config.yaml`에 Keeper에서 한 번만 표시한 bearer token 문자열을 직접
넣을 수 있습니다. 이 값은 Management Center API 응답에 노출되지 않고,
화면에는 구성 여부만 표시됩니다.

```yaml
usage-export:
  keeper:
    token: "<Keeper에서-복사한-Bearer-token>"
```

직접 token을 사용하는 경우:

- `token-env` 행은 제거하거나 주석 처리합니다. 둘을 동시에 설정할 수 없습니다.
- YAML 따옴표 안에 token 전체를 그대로 붙여 넣습니다. `Bearer ` 접두사는
  추가하지 않습니다.
- `config.yaml` 권한을 `0600`으로 설정하고, 파일을 저장소·백업 로그·화면
  공유에 포함하지 마세요.
- token을 잃어버리거나 노출했다면 Keeper의 **Settings > CPA Instances >
  Manage**에서 해당 credential을 revoke한 뒤 새 credential을 발급합니다.

## 3. CPA exporter 설정 추가

원격 CPA에서는 exporter를 처음에 비활성화한 상태로 설정합니다.

```yaml
usage-statistics-enabled: true

usage-export:
  enabled: false
  mode: disabled
  keeper:
    # HTTP와 HTTPS를 모두 사용할 수 있습니다.
    url: "http://192.168.0.50:8080"

    # 인터넷 또는 신뢰 경계 밖의 Keeper에는 HTTPS를 사용합니다.
    # url: "https://keeper.example.internal"
    # 아래 둘 중 정확히 하나만 설정합니다.
    token-env: "CPA_KEEPER_INGEST_TOKEN"
    # token: "<Keeper에서-복사한-Bearer-token>"

    # HTTP에서는 이 값을 생략하거나 null로 둡니다.
    ca-file: null
    client-cert-file: null
    client-key-file: null

  outbox:
    # 반드시 영속적이어야 하며, 이 CPA 전용으로 다른 CPA와 공유하면 안 됩니다.
    path: "/var/lib/cli-proxy-api/keeper-export/outbox.db"
    max-bytes: 1073741824

  delivery:
    max-batch-events: 500
    max-batch-bytes: 1048576
    flush-interval-ms: 1000
    request-timeout-ms: 15000
    initial-backoff-ms: 1000
    max-backoff-ms: 60000

  metadata:
    enabled: true
    interval-ms: 300000
    categories:
      - auth_files
      - api_keys
      - provider_identities

  privacy:
    include-client-ip: false
    include-forwarded-for: false
    include-user-agent: false
```

`keeper.url`에는 Keeper의 base URL만 지정합니다. `/api/v1/export/...`를
붙이지 마세요. CPA가 export endpoint 경로를 자동으로 추가합니다.

### Windows outbox 경로

Windows에서 CPA를 실행한다면 `outbox.path`에는 Windows 절대 경로를 사용합니다.
YAML의 backslash escape 문제를 피하려면 forward slash를 쓰거나 single quote로
감싸는 방식을 권장합니다.

```yaml
usage-export:
  outbox:
    # 권장: forward slash를 사용한 drive 절대 경로
    path: "C:/ProgramData/CLIProxyAPI/keeper-export/outbox.db"

    # 또는 single quote로 감싼 backslash 경로
    # path: 'D:\CLIProxyAPI\keeper-export\outbox.db'

    # UNC 공유 경로도 절대 경로로 사용할 수 있습니다.
    # path: '\\keeper-host\outbox\cli-proxy-a.db'
```

### 상대 outbox 경로

절대 경로 대신 상대 경로도 사용할 수 있습니다. 상대 경로는 **CPA
`config.yaml`이 있는 디렉터리**를 기준으로 해석되며, CPA의 현재 작업 디렉터리와는
무관합니다.

```yaml
usage-export:
  outbox:
    # config.yaml이 C:/CLIProxyAPI/config.yaml이면
    # C:/CLIProxyAPI/outbox.db로 해석됩니다.
    path: "outbox.db"
```

`C:keeper-outbox.db`는 drive-relative 경로라 의도와 다를 수 있으므로 Windows에서도
위와 같은 일반 상대 경로나 `C:/...` 형태의 drive 절대 경로를 사용하세요. outbox
파일은 CPA별로 분리하고, 서비스 계정에 해당 디렉터리의 생성·쓰기 권한을 부여하세요.
중첩 상대 경로(`keeper-export/outbox.db`)를 사용할 때는 상위 디렉터리를 미리
만들어 두어야 합니다.

### HTTP와 외부 네트워크

CPA는 기술적으로 모든 주소의 `http://`와 `https://` URL을 허용합니다. 그러나
HTTP는 bearer token, usage 이벤트와 metadata를 **암호화하지 않고 전송**합니다.
따라서 HTTP는 방화벽으로 격리된 신뢰 가능한 사설 LAN 또는 보안 터널 내부에서만
사용하세요.

인터넷, 공용 IP, 다른 조직/클라우드 계정, VPN 밖의 서버처럼 신뢰 경계를 넘는
경로에서도 CPA는 `http://` URL을 **차단하지 않으며 사용할 수 있습니다**. 다만
token과 usage가 평문이 되는 위험을 감수해야 하므로, 실운영에서는 HTTPS를 강력히
권장합니다. Keeper 앞에 Nginx, Caddy, Traefik 등의 TLS reverse proxy를 두고
HTTPS로 노출하는 방식을 권장합니다.
private CA 또는 mTLS를 사용하는 HTTPS 환경에서는 `ca-file` 및 짝을 이루는
`client-cert-file` / `client-key-file`을 설정합니다.

## 4. 전송 활성화 전 연결 테스트

CPA Management Center에서 비활성화 상태의 설정을 저장한 뒤
**Test Connection**을 실행합니다.

이 테스트는 outbox를 bind하거나 usage 데이터를 전송하지 않고, Keeper URL,
설정된 TLS, token 해석, credential scope 및 credential에 연결된 instance
identity를 검증합니다.

## 5. legacy pull에서 push로 전환

같은 CPA에서 legacy pull과 push export를 동시에 활성화하면 같은 usage record가
두 번 수집될 수 있으므로 함께 사용하면 안 됩니다.

CPA마다 다음 순서로 전환합니다.

1. request traffic을 drain하거나 중지합니다.
2. 해당 CPA의 legacy pull collector를 마무리하고 비활성화합니다.
3. CPA 설정을 다음과 같이 변경합니다.

   ```yaml
   usage-export:
     enabled: true
     mode: push
   ```

4. 설정을 저장하거나 CPA를 재시작합니다.
5. Keeper에서 의도한 instance가 healthy 상태인지, identity, ACK, backlog
   상태가 정상인지 확인합니다.
6. request traffic을 다시 시작합니다.

## 6. 기존 instance 관리

Keeper에서 다음 메뉴를 엽니다.

```text
Settings > CPA Instances > Manage
```

instance를 비활성화/재활성화하거나 export credential을 revoke할 수 있습니다.
이 동작은 usage 및 metadata 이력을 보존합니다. `Legacy`는 기존 pull 경로가
사용하는 고정 namespace이므로 삭제 대상이 아닙니다.
