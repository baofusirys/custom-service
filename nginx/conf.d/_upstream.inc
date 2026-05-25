    # ====== 反代规则（HTTP/HTTPS 共用） ======
    #
    # 关键设计：所有 proxy_pass 都用变量（$admin_host / $widget_host / $backend_host），
    # 让 nginx 每次请求向 Docker DNS（127.0.0.11，已在 nginx.conf http 块声明 resolver）
    # 动态解析 service 名到当前容器 IP。
    # 这样可以解决「docker compose 重启后容器换 IP，nginx upstream 静态解析旧 IP 把请求
    # 转到错误容器」的问题。

    limit_conn conn_per_ip 200;

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
        # 重声明 server 块那几条（覆盖原 SAMEORIGIN 同时不丢 HSTS / nosniff / Referrer-Policy）
        add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
        add_header X-Content-Type-Options "nosniff" always;
        add_header Referrer-Policy "strict-origin-when-cross-origin" always;
        # widget loader.js / chat.html 要被任意域 fetch + iframe，CORS 允许全部
        add_header Access-Control-Allow-Origin "*" always;
        # 故意不加 X-Frame-Options（这就是这次的关键修复）
    }

    # 登录接口：严格限速（防爆破）
    location = /api/agent/login {
        limit_req zone=login_rps burst=5 nodelay;
        set $backend_host backend;
        proxy_pass http://$backend_host:8080$request_uri;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # 一般 API
    location /api/ {
        limit_req zone=api_rps burst=40 nodelay;
        set $backend_host backend;
        proxy_pass http://$backend_host:8080$request_uri;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
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
        proxy_buffering off;
    }

    # WebSocket — 必须 Upgrade + 关闭超时
    location /ws/ {
        limit_req zone=ws_rps burst=20 nodelay;
        set $backend_host backend;
        proxy_pass http://$backend_host:8080$request_uri;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        # 长连接不能让 Nginx 超时干掉
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }

    # 默认根路径：跳到管理后台登录
    location = / {
        return 302 /admin/;
    }
