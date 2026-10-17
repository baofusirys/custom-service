# 多人大厅媒体服务（cs-livekit）生产部署手册（[119]，北京时间 2026-10-18）

> 适用：生产机 `49.233.156.149`（2 核 7.5G，与 fakami 主站共用），cs 服务编排在 fakami 项目的 `/fakami/docker-compose.yml` 内，版本由 `/fakami/.env` 的 `IMAGE_TAG` 控制。
> 接口契约见 `_internal/reports/hall-contract.md` 第 7 节；镜像源码见仓库 `livekit/`。本文只写变量名，不含任何凭证值。
> 本手册由本地编写，**未连接生产核对**：落地前必须对照 `/fakami/docker-compose.yml` 里现有 `cs-backend` / `cs-coturn` 段核对锚点名、网络写法与变量名（见第 9 节核对清单）。

## 1. 组成与资源

| 项 | 值 |
| --- | --- |
| 镜像 | `${REGISTRY_BASE}/cs-livekit:${IMAGE_TAG}`（CI 矩阵 `build-images.yml` 已加入，`promote-acr.yml` 同步；`FROM livekit/livekit-server:v1.13.10` 固定版本） |
| 运行用户 | UID/GID 10001（官方镜像默认 root，本镜像已切换）；`cap_drop: [ALL]`、`no-new-privileges` |
| 端口 | 7880 信令只在容器内网（cs-nginx 反代 `/lk/rtc`，后端走 `http://cs-livekit:7880`）；**7881/tcp、7882/udp 对公网**（宿主端口必须与容器端口一致，ICE 候选里通告的就是这两个端口） |
| 资源 | 生产 `mem_limit: 384m`、`cpus: "0.75"`（仓库自带 compose 为 512m / 1.0） |
| 健康检查 | 容器内 busybox `wget` 探测 `http://127.0.0.1:7880/`（官方镜像基于 alpine，自带 wget，无需装 curl） |
| 日志 | 容器 stdout 为 LiveKit JSON 日志（json-file 20m×5 轮转，仅作临时排障副本）；大厅业务与审计事件（入厅、开麦、移出、回调、对账、清理）由 cs-backend 写入 `/srv/cs-data/logs/backend/` 长期留存 |
| 密钥 | 入口脚本由 `LIVEKIT_API_KEY` / `LIVEKIT_API_SECRET` 组装 `LIVEKIT_KEYS`，webhook 签名 key 走 `LIVEKIT_WEBHOOK_API_KEY`，不打印任何值；两项都为空时容器待机（大厅关闭），只填一项或格式不对直接 FATAL 退出 |
| 公网 IP | `NODE_IP` 复用 `CS_TURN_EXTERNAL_IP`；缺失、回环、0.0.0.0、组播等直接拒绝启动，私网地址告警 |

## 2. 新增 .env 变量（`/fakami/.env`）

| 变量 | 用途 | 生成方式 |
| --- | --- | --- |
| `CS_LIVEKIT_API_KEY` | LiveKit API Key（JWT 的 iss，3–64 位字母/数字/`_`/`-`） | `echo "CS_LIVEKIT_API_KEY=API$(openssl rand -hex 8)" >> /fakami/.env` |
| `CS_LIVEKIT_API_SECRET` | LiveKit API Secret（≥32 字符，只用 hex/base64 字符） | `echo "CS_LIVEKIT_API_SECRET=$(openssl rand -hex 32)" >> /fakami/.env` |
| （复用）`CS_TURN_EXTERNAL_IP` | 服务器公网 IP → 容器内 `NODE_IP` | 已存在，不新增 |

- 直接在服务器上用 `openssl rand` 现场生成并追加写入，**不要 echo 到终端、不要复制到聊天或文档**；生成后 `chmod 600 /fakami/.env`。
- 这两个值是本项目独立的一份，禁止与 JWT_SECRET、TURN 密钥等任何其它密钥复用。
- 改完 `.env` 按灾备规则把 `/fakami/.env` 纳入本地加密副本（7z 加密）留存。
- 若生产 cs 服务还通过 `/srv/cs-data/.env` 读取变量（LATEST 记录 IMAGE_TAG 两处同步），以 `/fakami/docker-compose.yml` 中 cs-backend 的实际 `environment` / `env_file` 写法为准，保持两处一致。

## 3. 可直接粘贴的 cs-livekit 片段（放在 `services:` 下、cs-coturn 段附近）

```yaml
  # [119] 多人大厅媒体服务（自托管 LiveKit v1.13.10，UID 10001 非 root）
  # 信令 7880 只在 cs-net 内网；媒体 7881/tcp、7882/udp 对公网（腾讯云防火墙/安全组需放行），宿主端口不得改成别的值
  cs-livekit:
    <<: *service-standard
    image: ${REGISTRY_BASE:?REGISTRY_BASE 未设置}/cs-livekit:${IMAGE_TAG:?IMAGE_TAG 未设置}
    container_name: cs-livekit
    restart: unless-stopped
    environment:
      TZ: Asia/Shanghai
      LIVEKIT_API_KEY: ${CS_LIVEKIT_API_KEY:?CS_LIVEKIT_API_KEY 未设置}
      LIVEKIT_API_SECRET: ${CS_LIVEKIT_API_SECRET:?CS_LIVEKIT_API_SECRET 未设置}
      NODE_IP: ${CS_TURN_EXTERNAL_IP:?CS_TURN_EXTERNAL_IP 未设置}
    ports:
      - "7881:7881/tcp"
      - "7882:7882/udp"
    networks:
      cs-net:
        aliases: [livekit]
    mem_limit: 384m
    cpus: "0.75"
    security_opt:
      - "no-new-privileges:true"
    cap_drop: [ALL]
    logging:
      driver: "json-file"
      options:
        max-size: "20m"
        max-file: "5"
    healthcheck:
      test: ["CMD", "/usr/local/bin/cs-livekit-healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 60s
```

说明：
- `<<: *service-standard` 合并锚点里的通用项；片段里显式写出的 `restart` / `logging` / `security_opt` 等若与锚点重复，以片段为准（YAML 合并键规则：显式键覆盖锚点），可按锚点实际内容删去重复项。
- `${VAR:?…}`：变量缺失时 `docker compose` 在**解析阶段**就报错退出，不会改动任何正在运行的容器（宁可部署失败也不悄悄关掉大厅）。
- 不需要 `depends_on`：cs-nginx 用变量 `proxy_pass`（每次请求经 Docker DNS 解析），cs-livekit 未就绪时 `/lk/rtc` 返回 502，其它功能不受影响；后端对 LiveKit 不可用有降级（503 `50370` + 对账兜底）。
- 回调地址写在镜像内 `livekit.yaml`：`http://cs-backend:8080/api/livekit/webhook`（cs-net 内用容器名直连，不经过 nginx）。

## 4. cs-backend 需追加的 LiveKit 映射（追加到现有 `environment:`）

```yaml
      # [119] 多人大厅（LiveKit）
      LIVEKIT_API_KEY: ${CS_LIVEKIT_API_KEY:?CS_LIVEKIT_API_KEY 未设置}
      LIVEKIT_API_SECRET: ${CS_LIVEKIT_API_SECRET:?CS_LIVEKIT_API_SECRET 未设置}
      LIVEKIT_INTERNAL_URL: http://cs-livekit:7880
      # 不写 LIVEKIT_PUBLIC_URL：默认 wss://<PUBLIC_DOMAIN>/lk（PUBLIC_DOMAIN 为中文域名时后端自动转 punycode）
```

- 后端启动自检：Key 不合法/示例值、Secret 少于 32 字符或命中占位黑名单、地址不合法 → `[FATAL]` 只打印变量名并退出；两项都不配 → WARN「多人大厅关闭」，原有功能不受影响。
- cs-nginx、cs-admin、cs-widget 无需新增变量（升级镜像即带上 `/lk/rtc` 反代、`/api/livekit/` 404 与挂件 CSP 调整）。
- 生产入口经 fakami-caddy 反代到 cs-nginx：请确认 caddy 把客服域名的**全部路径**（含 `/lk/`）转给 cs-nginx（caddy `reverse_proxy` 自动支持 WebSocket，无需额外配置）。

## 5. 腾讯云放行 TCP 7881 / UDP 7882

先在控制台确认实例类型（轻量应用服务器 Lighthouse 或云服务器 CVM），只做对应的一种：

**A. 轻量应用服务器 → 「防火墙」**
1. 控制台 → 轻量应用服务器 → 点击实例 → 「防火墙」页签 → 「添加规则」。
2. 规则 1：应用类型「自定义」，来源 `0.0.0.0/0`（全部 IPv4），协议 **UDP**，端口 **7882**，策略「允许」，备注「cs-livekit 媒体 UDP」。
3. 规则 2：应用类型「自定义」，来源 `0.0.0.0/0`，协议 **TCP**，端口 **7881**，策略「允许」，备注「cs-livekit 媒体 TCP 兜底」。
4. 保存后在列表中确认两条规则均为「允许」。**不要**放行 7880（信令只走 443）。

**B. 云服务器 CVM → 「安全组」**
1. 控制台 → 云服务器 → 实例详情 → 「安全组」页签，找到实例绑定的安全组 → 「编辑规则」（或 网络与安全 → 安全组 → 对应安全组 → 修改规则）。
2. 「入站规则」→「添加规则」：类型「自定义」，来源 `0.0.0.0/0`，协议端口 `UDP:7882`，策略「允许」，备注「cs-livekit 媒体 UDP」。
3. 再添加：来源 `0.0.0.0/0`，协议端口 `TCP:7881`，策略「允许」，备注「cs-livekit 媒体 TCP 兜底」。
4. 出站规则保持默认全放通（若已收紧，需放行 UDP 出站，否则媒体回包不通）。

> **宿主机防火墙（生产实测必须做）**：生产机 iptables 的 `DOCKER-USER` 链由 fakami 加固（`/etc/iptables/rules.v4`，EdgeOne 规则由 `/fakami/ops/edgeone-firewall/firewall.py` 维护）以 `-j DROP` 结尾，所有 Docker 映射端口默认对公网丢弃。只放行腾讯云规则不够，还须在该链末尾 DROP 之前加入（实时 + 写入 rules.v4，`iptables-restore --test` 校验），详见第 10 节：
> `-A DOCKER-USER -p udp -m conntrack --ctdir ORIGINAL --ctorigdstport 7882 -m comment --comment cs-livekit-media -j RETURN`
> `-A DOCKER-USER -p tcp -m conntrack --ctdir ORIGINAL --ctorigdstport 7881 -m comment --comment cs-livekit-media -j RETURN`
>
> 宿主机若启用了 ufw/firewalld：Docker 端口映射走 iptables DNAT，不受 ufw 默认链约束。放行后与 `docker compose ps` 的 ports 清单逐一核对：对公网的应只有 80/443、22、coturn 既有端口与新增的 7881/tcp、7882/udp。

## 6. 部署步骤（沿用生产既有做法：只改 IMAGE_TAG、只更新 cs 服务）

1. 前置只读检查：`df -h /`（可用 ≥20% 且 ≥5GB）、`timedatectl`（NTP synchronized=yes、Asia/Shanghai）、`docker compose -f /fakami/docker-compose.yml ps`。
2. 手动备份数据库到 `/srv/cs-backup/manual/cs-before-<版本>-<北京时间>.sql.gz` 并 `gzip -t` 校验、确认大小 > 0（迁移 015 运行前后端也会自动备份到 `pre-migrate/`）。
3. 按第 2 节生成两个变量；按第 3、4 节修改 `/fakami/docker-compose.yml`；`docker compose -f /fakami/docker-compose.yml config -q` 校验语法（只输出错误，不回显变量值）。
4. `/fakami/.env`（及需要同步的 `/srv/cs-data/.env`）的 `IMAGE_TAG` 改为本次发布版本；**爷爷本地 fakami 项目的 .env 同步改**，否则下次部署 fakami 会回退客服版本。
5. 腾讯云按第 5 节放行端口。
6. 只拉取并更新 cs 服务：`cd /fakami && docker compose pull cs-backend cs-admin cs-widget cs-nginx cs-livekit && docker compose up -d cs-backend cs-admin cs-widget cs-nginx cs-livekit`（mysql/redis/coturn 镜像若随版本更新也按既有做法一并列出）。
7. 验证通过后 `docker image prune -f`、`docker builder prune -f`；严禁任何带 `-v` / `volume prune` 的命令。

## 7. 部署后验收（只读命令，结果如实记录到 `_internal/verification/deploy-*.txt`）

```bash
cd /fakami
docker compose ps cs-livekit cs-backend cs-nginx            # 全部 Up 且 healthy（cs-livekit start_period 60s）
docker inspect cs-livekit --format 'user={{.Config.User}} mem={{.HostConfig.Memory}} nanocpus={{.HostConfig.NanoCpus}} sec={{.HostConfig.SecurityOpt}} capdrop={{.HostConfig.CapDrop}} log={{.HostConfig.LogConfig}}'
docker exec cs-livekit id                                   # uid=10001 gid=10001
docker exec cs-livekit date                                 # 北京时间 CST +0800，与宿主机 date 一致
docker logs --tail 100 cs-livekit 2>&1 | grep -E '"event":"livekit_starting"|"level":"(FATAL|error)"'   # 有 livekit_starting，无 FATAL
ss -tlnup | grep -E ':(7880|7881|7882)\b'                   # 只应看到 7881/tcp、7882/udp；7880 不得出现在宿主机
docker stats --no-stream cs-livekit                          # MEM LIMIT 384MiB

D=https://<客服域名>
curl -s -o /dev/null -w '%{http_code}\n' "$D/lk/rtc/validate"                                 # 4xx 且响应来自 LiveKit（缺少令牌）即已反代成功；502=cs-livekit 未就绪；404=nginx 未更新
curl -s -o /dev/null -w '%{http_code}\n' -X POST "$D/lk/twirp/livekit.RoomService/ListRooms"  # 404（服务端 API 不对外）
curl -s -o /dev/null -w '%{http_code}\n' -X POST "$D/api/livekit/webhook"                     # 404（回调只允许内网直连）
curl -s "$D/api/visitor/settings" | grep -o '"hall_[a-z_]*":[a-z]*'                           # hall_enabled:true、hall_video_enabled:false
curl -sI "$D/widget/chat.html" | grep -i content-security-policy                               # 含 media-src 'self' blob: 与 wss://<域名>/lk/ wss://<域名>/ws/
curl -s -o /dev/null -w '%{http_code}\n' -X POST "$D/api/visitor/hall/join"                   # 401（未带访客令牌）

grep -E '"hall_livekit_configured"|"015_hall"' /srv/cs-data/logs/backend/business.log | tail -5   # 配置生效、迁移 015 成功
docker compose logs --tail=200 cs-backend cs-nginx cs-livekit 2>&1 | grep -E 'FATAL|panic|ERROR'  # 应无输出
```

外网连通（爷爷本地电脑 PowerShell 7）：`Test-NetConnection 49.233.156.149 -Port 7881`（TcpTestSucceeded=True）；UDP 无法用该命令验证，以浏览器实测为准：两台设备打开带挂件的网页 → 自动进入大厅 → 一方开麦另一方能听到；随后在 `business.log` 看到 `hall_participant_joined` 回调事件，`docker logs cs-livekit` 中该参与者的 ICE 候选为 udp 7882（UDP 被拦时会退到 tcp 7881）。

## 8. 回滚

1. **镜像回退**：`/fakami/.env` 的 `IMAGE_TAG` 改回上一版（0.8.2），注释掉 cs-livekit 段与 cs-backend 的 4 行 LiveKit 映射（0.8.2 没有 cs-livekit 镜像，不注释会拉取失败），然后 `docker compose up -d cs-backend cs-admin cs-widget cs-nginx`，再 `docker compose stop cs-livekit && docker compose rm -f cs-livekit`（无数据卷，删除容器不丢数据）。
2. **迁移 015**：只新增 `hall_messages` 表和 5 个设置项，旧版本代码不读取，**回退镜像时不必执行 down**。确需彻底移除时（有损：大厅聊天记录全部丢失）：先按第 6 节第 2 步手动备份，再在 cs-mysql 中执行仓库 `backend/migrations/down/015_hall.sql`，并同时执行 `DELETE FROM schema_migrations WHERE version='015_hall';`，否则再次升级不会重建表。大厅附件文件记录（`files.conv_id='hall_default'`）按 down 脚本注释人工清理。
3. **端口**：回滚后在腾讯云删除 7881/tcp、7882/udp 两条规则，`ss -tlnup` 确认不再监听。
4. **只关大厅不回滚版本**：管理后台「系统设置 → 多人大厅」关闭，或清空两个 `CS_LIVEKIT_*` 变量（compose 片段需改为 `${CS_LIVEKIT_API_KEY:-}` 写法）后重建 cs-backend 与 cs-livekit（后端大厅关闭、cs-livekit 待机）。

## 9. 落地前核对清单（本地无法核实，部署执行人逐项确认）

- [ ] `/fakami/docker-compose.yml` 的通用锚点名确为 `service-standard`，cs 服务网络写法（`networks: cs-net: aliases:`）与本片段一致。
- [ ] cs-backend 在 cs-net 中的容器名/别名能解析为 `cs-backend`（livekit.yaml 回调地址依赖它），cs-livekit 能解析为 `cs-livekit`（cs-nginx 与后端依赖它）。
- [ ] `CS_TURN_EXTERNAL_IP` 是公网 IP（不是 10.x / 172.16–31.x 内网 IP）。
- [ ] 内存余量：`free -h` 与 `docker stats --no-stream` 汇总后，新增 384m 不使全部容器 limit 之和超过物理内存 80%。
- [ ] fakami-caddy 把客服域名全部路径转发给 cs-nginx，且未单独拦截 `/lk/`。
- [ ] 带宽：公网 7Mbps 与主站共用，默认上限（访客同时开麦 6、摄像头 2、每人订阅 2 路视频、360p+180p 联播）不要随意调大。
- [ ] 可选：LiveKit 日志若提示 UDP 接收缓冲区偏小（`net.core.rmem_max`），属于宿主机 sysctl 调优，需单独征得同意后作为一次性宿主机操作处理，不混入部署流程。

## 10. 生产实际落地记录（北京时间 2026-10-18）

- **版本**：v0.9.1（v0.9.0 的 cs-admin 构建因 npmmirror 缺 livekit-client 2.23.0 失败，改为失败回退官方源后发 0.9.1）。01:13 部署，部署前手动备份 `/srv/cs-backup/manual/cs-before-0.9.1-20261018-005706.sql.gz`（5454660 字节，gzip -t 通过）及编排、.env 副本（600）。迁移 015 自动执行（迁移前自动备份成功）。
- **cs-livekit 镜像**：CI 已推送到阿里云 ACR，但新仓库 `baofusir/cs-livekit` 默认私有，服务器匿名拉取被拒。现做法：在服务器上用本仓库 `livekit/`（提交 dbf2d19）`docker build` 出 `${REGISTRY_BASE}/cs-livekit:0.9.1`；`/fakami/docker-compose.yml` 的 cs-livekit 段标签改用独立变量 `CS_LIVEKIT_IMAGE_TAG`（/fakami/.env，当前 0.9.1）并加 `pull_policy: never`，因此以后只升级 `IMAGE_TAG` 不会因找不到 cs-livekit 新标签而失败。
  - ACR 仓库改为「公开」后：删除 pull_policy 行、image 标签改回 `IMAGE_TAG`，与其它 cs-* 一致。
  - 保持本机构建时，`livekit/` 有改动才需要：上传该目录到服务器临时目录 → `nice -n 10 docker build -t <REGISTRY_BASE>/cs-livekit:<新版本> .` → 改 `CS_LIVEKIT_IMAGE_TAG` → `docker compose up -d --no-deps cs-livekit`。
- **端口**：轻量应用服务器 `lhins-rba8ijgo`（ap-beijing-6）防火墙已新增 UDP 7882、TCP 7881（来源 0.0.0.0/0，备注「客服多人大厅语音」）；宿主机 `DOCKER-USER` 已按上文加两条 RETURN 并写入 rules.v4（改前备份 `/srv/cs-backup/manual/host-iptables-*-before-livekit-20261018-012701*`）。服务器无公网 IPv6，rules.v6 未改。
- **验收**：8 个 cs 容器 healthy、fakami 业务容器不受影响；外网（家中 Mac）TCP 7881 可连；用临时探测房间两人实连生产 LiveKit：发布方 ICE 选中 `udp 49.233.156.149:7882`，接收方 3 秒内音频字节持续增长；LiveKit 回调已到达后端并验签通过（探测房间按设计忽略）。
- **既有问题（未处理，待爷爷决定）**：同一 `DOCKER-USER` DROP 也拦着 coturn 的 3478/5349/49152-49200，外网实测 TCP 3478 不通，一对一语音的 TURN 中继对公网实际不可用（与本次大厅无关，大厅走 LiveKit 自己的 7881/7882）。如需恢复，按同样方式为这些端口加 RETURN 规则。
