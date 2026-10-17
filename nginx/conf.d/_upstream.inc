    # ====== 反代规则（HTTP/HTTPS 共用） ======
    #
    # 关键设计：所有 proxy_pass 都用变量（$admin_host / $widget_host / $backend_host），
    # 让 nginx 每次请求向 Docker DNS（127.0.0.11，已在 nginx.conf http 块声明 resolver）
    # 动态解析 service 名到当前容器 IP。
    # 这样可以解决「docker compose 重启后容器换 IP，nginx upstream 静态解析旧 IP 把请求
    # 转到错误容器」的问题。

    # 管理后台静态文件：把 /admin/ 反代到 admin 容器
    # 变量 proxy_pass 时 nginx 不做 URI 重写，所以用 rewrite ... break; 去掉 /admin 前缀
    location /admin/ {
        set $admin_host admin;
        rewrite ^/admin(/.*)$ $1 break;
        proxy_pass http://$admin_host:80;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Request-ID $request_id;
        proxy_set_header X-Trace-ID $request_id;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # 嵌入式 widget 静态文件
    # [046] widget 必须可被任意第三方网站 iframe 嵌入（这是 widget 存在的意义），
    # 而 ssl.conf.template server 块全局 add_header X-Frame-Options "SAMEORIGIN" 会被这里
    # 继承导致跨域嵌入被浏览器拒。
    # nginx add_header 是块级**覆盖**（不是合并）：location 一旦声明 add_header，
    # server 块所有 add_header 全部失效。所以要把 server 那几条安全 header 在这里
    # 重新声明一遍（除 X-Frame-Options 故意不加），既允许跨域嵌入又不丢安全基线。
    location /widget/ {
        set $widget_host widget;
        rewrite ^/widget(/.*)$ $1 break;
        proxy_pass http://$widget_host:80;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Request-ID $request_id;
        proxy_set_header X-Trace-ID $request_id;
        # 重声明 server 块那几条（覆盖原 SAMEORIGIN 同时不丢 HSTS / nosniff / Referrer-Policy）
        add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
        add_header X-Content-Type-Options "nosniff" always;
        add_header Referrer-Policy "strict-origin-when-cross-origin" always;
        # [119] 多人大厅：media-src 放行 blob:（LiveKit 以 MediaStream 播放 / 提示音）；worker-src 放行 blob:（与 cs-widget 自身 CSP 一致，
        # 两份 CSP 浏览器取交集）；connect-src 从任意 wss: 收紧为同源 wss 且限定路径 /lk/（LiveKit 信令）与 /ws/（访客实时连接），
        # 来源由 nginx.conf 的 map $cs_same_origin_wss 按白名单生成（不合法的 Host 不追加任何来源，只剩 'self'）。
        add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self' $cs_same_origin_wss; worker-src 'self' blob:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors *" always;
        # 故意不加 X-Frame-Options（这就是这次的关键修复）
    }

    # 登录限流与失败锁定由 backend 统一执行，按真实 IP 和账号维度计数。
    location = /api/agent/login {
        set $backend_host backend;
        proxy_pass http://$backend_host:8080$request_uri;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Request-ID $request_id;
        proxy_set_header X-Trace-ID $request_id;
    }

    # [119] LiveKit 回调只允许 cs-livekit 在容器内网直连 http://cs-backend:8080/api/livekit/webhook；
    # 公网入口一律 404（后端对带 X-Forwarded-For / X-Real-IP 的回调同样 404，双保险）。
    # 编码绕过（%6c、%2f、//）在 location 匹配前已被 nginx 解码与合并，同样落到这里。
    location ^~ /api/livekit/ {
        return 404;
    }
    location = /api/livekit {
        return 404;
    }

    # API 业务限流由 backend 按动作和身份维度执行。
    location /api/ {
        set $backend_host backend;
        proxy_pass http://$backend_host:8080$request_uri;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Request-ID $request_id;
        proxy_set_header X-Trace-ID $request_id;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 60s;
        proxy_send_timeout 60s;
    }

    # 文件直出
    location /files/ {
        set $backend_host backend;
        proxy_pass http://$backend_host:8080$request_uri;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Request-ID $request_id;
        proxy_set_header X-Trace-ID $request_id;
        proxy_buffering off;
    }

    # [062] WebSocket 移除按 IP 限速；Upgrade + 关闭超时仍保留
    location /ws/ {
        set $backend_host backend;
        proxy_pass http://$backend_host:8080$request_uri;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Request-ID $request_id;
        proxy_set_header X-Trace-ID $request_id;
        # 长连接不能让 Nginx 超时干掉
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }

    # [119] 多人大厅：LiveKit 信令 WebSocket（/lk/rtc… → http://cs-livekit:7880/rtc…，含 /rtc/validate、/rtc/v1）。
    # 只放行 /lk/rtc 及其子路径（字符白名单），/lk/ 下其它路径（含 /twirp 服务端 API）一律 404。
    # 限流写法沿用 /ws/：不做按 IP 限流（[062]/[066] 已去掉全部按 IP 限流，NAT 共用出口 IP 会误伤）；
    # 入场令牌由后端按身份限流签发，LiveKit 自身校验令牌签名与房间权限。
    # 访问日志只记 $uri（nginx.conf 的 log_format 不记 query），URL 中的 access_token 不会落盘。
    location ~ ^/lk(?<lk_path>/rtc(?:/[A-Za-z0-9._-]+)*)$ {
        set $livekit_host cs-livekit;
        proxy_pass http://$livekit_host:7880$lk_path$is_args$args;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Request-ID $request_id;
        proxy_set_header X-Trace-ID $request_id;
        proxy_buffering off;
        # 信令长连接 1 小时（与 /ws/ 一致），LiveKit 客户端自带心跳与断线重连
        proxy_connect_timeout 5s;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }

    location /lk/ {
        return 404;
    }
    location = /lk {
        return 404;
    }

    # 默认根路径：跳到管理后台登录
    location = / {
        return 302 /admin/;
    }
