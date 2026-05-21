FROM mysql:8.0
# 北京时间
ENV TZ=Asia/Shanghai
# 把自定义 my.cnf COPY 进镜像（用户铁律：Dockerfile 构建，不允许 volume 挂静态文件）
COPY my.cnf /etc/mysql/conf.d/cs-custom.cnf
RUN chmod 644 /etc/mysql/conf.d/cs-custom.cnf
