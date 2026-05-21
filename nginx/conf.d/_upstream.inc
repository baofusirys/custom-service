    # ====== 反代规则（HTTP/HTTPS 共用） ======

    limit_conn conn_per_ip 200;

    # 管理后台静态文件：把 /admin/ 反代到 admin 容器
    location /admin/ {
        proxy_pass http://cs_admin/;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # 嵌入式 widget 静态文件
    location /widget/ {
        proxy_pass http://cs_widget/;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }

    # 登录接口：严格限速（防爆破）
    location = /api/agent/login {
        limit_req zone=login_rps burst=5 nodelay;
        proxy_pass http://cs_backend;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # 一般 API
    location /api/ {
        limit_req zone=api_rps burst=40 nodelay;
        proxy_pass http://cs_backend;
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
        proxy_pass http://cs_backend;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_buffering off;
    }

    # WebSocket — 必须 Upgrade + 关闭超时
    location /ws/ {
        limit_req zone=ws_rps burst=20 nodelay;
        proxy_pass http://cs_backend;
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
