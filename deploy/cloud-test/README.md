# 云端独立测试环境

2026-11-01 用户明确授权在本机 SSH `lh-gz` 对应云服务器部署，手机使用
`https://tpandmr.xyz`。目录 `/opt/rai-cloud-test-20261101`，Compose project
`rai-cloud-test-20261101`，独立卷、内部数据库网络、随机分角色凭证。
仅新增 `/v2/`、`/ready`、`/health`；原 `/v1/` 配置保持原样。

服务为 PostgreSQL 18.6、同镜像 Go API/Worker；19 个迁移显式执行。
`compose.yaml` 是 release 模板的资源覆盖，针对已有其他服务的 2 GiB 主机。
数据库不发布端口，API 仅 `127.0.0.1:18443`；Nginx 与 API 间也验证 TLS
证书链及主机名。Nginx 验证深度显式为 4（默认 1 无法验证当前证书链）。
`nginx-locations.conf` 仅包含应加入既有 HTTPS server 的 location。
未新增 Web 客户端，根路径继续返回 404。

运维（SSH 登录后）：

```sh
cd /opt/rai-cloud-test-20261101
./dc ps
./dc exec -T api /rai probe --ready
./dc exec -T worker /rai worker-ready
./dc stop api worker
./dc start api worker
```

手机 App 设置 → 开发接口：服务地址填 `https://tpandmr.xyz`，采集器周期标识
和设备令牌从本机 Git 忽略的 `.local/cloud-test-20261101/手机连接配置.txt`
取得，再点“建立独立同步会话”。该文件及 enrollment.json 权限0600，目录0700。
服务器 enrollment 在私有 `enrollment/enrollment.json`；不经公网发布凭证。

当前证书到期日为 2026-12-01（UTC），沿用现有 Certbot 入口。API 使用证书副本，
证书续期后需显式更新 secrets/tls_cert 和 secrets/tls_key、保持 root:65532/0640
私钥权限，然后 `./dc restart api` 并验证 probe。没有把既有续期流程改为自动重启。

镜像仓库下载超时后，从 NAS 仅导出已有 PostgreSQL 镜像（没有访问其数据库/卷）。
源镜像 registry digest 为 release 模板的 bbf835…20fc，13 个 RootFS layer
摘要逐个与云端导入结果相同；Docker 导入后的 image ID 为
`sha256:5700567fddf107fd83510813491485c11b54253f5237b00f7b9ef77fb51368db`，
本地 tag `rai-cloud-postgres:18.6-20261101`。应用由本地 Go1.27.3 交叉编译
linux/amd64 静态二进制，再用 scratch 构建，image ID 为
`sha256:6852e4bb67208082feb5f59985e5f7cdb906af64ebb8a30d4b3ebe1aa6cfeb99`。
这些是测试工件，不宣称正式发布镜像验收。

回退新增公网入口：移除 `/etc/nginx/sites-available/rai` 中
`include /etc/nginx/snippets/rai-cloud-test.conf;`，`nginx -t` 后 reload，
再 `./dc stop api worker`。保留数据库卷；不要运行 down --volumes。
改动前 Nginx 配置保存在部署目录 `nginx-rai.before`。
