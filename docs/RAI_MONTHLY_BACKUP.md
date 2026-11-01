# RAI 每月 PostgreSQL 云端备份

2026-10-29，Asia/Shanghai。本专项不是 P8；P7 历史结果和加密工具保留。本次选择**每月 1 日 03:00、SSH 传输、无额外文件加密、远端最近三份成功副本**。正式云服务器和定时任务未安装。当前部署与监控适配见 [部署结果](RAI_MONTHLY_BACKUP_DEPLOY_RESULT.md)；上一轮实现验收见 [Result](RAI_BACKUP_RECOVERY_RESULT.md)。

## 配置与运行环境

最新部署准备检查（2026-10-29）：fnOS Docker 28.5.2、Compose 5.1.0；只有旧 `rai` project 的 `rai-postgres`（镜像 PostgreSQL 18.6）与 `frpc`，没有已确认的 V2 API/Worker/业务库。旧库不作为备份源，也没有查询其 schema 或健康事实。`cron` active、宿主 `Asia/Shanghai`；当前用户无 crontab，可读系统 cron/timer 中未发现 RAI 月度备份。没有读取不可访问的其他用户任务，因此正式安装前仍须管理员核对。

`/home/tpandmr/rai/scripts/rai-backup.sh` 不存在，`/home/tpandmr/rai-v2` 不存在；本机源码入口为本仓库 `scripts/rai-backup.sh`，不能把一次性 P7 测试目录当安装位置。home 分区约119GB、`/vol1`约886GB可用（单次只读观测，不是云端容量验收）。正式 V2 未部署，本轮只准备可复用配置。当前结果见 [部署专项结果](RAI_MONTHLY_BACKUP_DEPLOY_RESULT.md)。

源端复用 `deploy/release/compose.yaml` 的 `postgres` 服务和 `rai_backup_login` 只读身份，通过原 `pg_backup_password` secret 连接 `rai_v2`；宿主需 Bash、Python 3.9+（含 IANA zoneinfo 数据）、Docker Compose、OpenSSH。云端只需 Python 3、POSIX 文件系统与 OpenSSH，不需要 PostgreSQL、API、Worker 或常驻备份容器。

复制 [配置样例](../deploy/release/monthly-backup.environment.example)，与已有 release 环境合并成仓库外 **0600** 配置文件，父目录 **0700**。必须填入：

| 配置 | 含义 |
|---|---|
| `RAI_PROJECT` / `RAI_SECRETS_DIR` | 明确的源发布项目与原 secret 目录 |
| `RAI_RELEASE_IMAGE` / `RAI_TLS_SERVER_NAME` | 现有 Compose 必填环境，复用原配置 |
| `RAI_BACKUP_SSH_HOST` / `PORT` / `USER` | 用户批准的目标主机名、端口、账户；没有默认地址或账户 |
| `RAI_BACKUP_SSH_KEY` | 仓库外绝对路径，当前用户所有的私有普通文件；不接受符号链接 |
| `RAI_BACKUP_KNOWN_HOSTS` | 可选绝对路径；空值使用 OpenSSH 标准 known_hosts。须人工核对主机指纹 |
| `RAI_BACKUP_REMOTE_DIR` | 绝对路径；其父目录必须已存在；工具可创建最后一级 0700 目录 |
| `RAI_BACKUP_STATE_DIR` | 源端绝对私有状态目录；父目录已存在；存锁和本次临时 dump |
| `RAI_BACKUP_TIMEZONE` | 必填且固定 `Asia/Shanghai` |
| `RAI_BACKUP_TIMEOUT_SECONDS` | 单个外部命令超时，默认 3600 秒 |
| `RAI_SOURCE_COMMIT` | 可选已审核源版本；未提供明确记 `UNRECORDED` |

不猜测 SSH 凭证。脚本禁用口令交互、agent forwarding 和 SSH 自动接受 host key；使用指定私钥及严格主机验证。不得配置公开 HTTP 下载目录。专用备份账户仍能读取未加密归档，按用户决定接受这个边界。

手动运行（配置文件是操作者信任的 shell 环境，不要粘贴未知来源内容）：

```bash
set -a
source /ABSOLUTE/private/monthly.env
set +a
/ABSOLUTE/rai/scripts/rai-backup.sh backup
/ABSOLUTE/rai/scripts/rai-backup.sh check
/ABSOLUTE/rai/scripts/rai-backup.sh list
```

`check` 和 `list` 都核验已发布副本的大小、摘要和 SHA sidecar；不执行保留清理。`list` 是清单，`check` 还判断月度期限与本机上一次备份尝试；非健康状态返回1，锁忙75。输出只含受控文件名、时间、大小和摘要，不含密码、健康事实或 GPS。完整 SHA 重读可能耗时，不建议每5分钟跨云扫描三份大归档；个人系统可手动或按已有低频运维入口检查，无新监控服务。

## 备份与发布

1. 本地非阻塞文件锁；计划任务与手动运行互斥。锁忙返回 75，记录 `already_running`。
2. 检查来源 schema 恰为 13；使用只读身份整库 `pg_dump -Fc --no-owner --no-acl --lock-wait-timeout=10s`。不按表或 Workout 过滤；包含 P3/P4/P5、用户历史和 goose 版本表。维护时禁止并发 schema migration。
3. 临时 dump 为 0600，目录 0700；检查 dump 返回码、非空、`pg_restore --list` 和 SHA256。
4. 通过 SSH 向远端 `.partial` 上传；远端独立核对接收字节数、流摘要以及实际文件重读摘要，并 fsync。
5. 同目录 rename 发布 dump、SHA sidecar，最后发布 JSON manifest；manifest 是整套副本的成功标记，逐次目录 fsync。任意中断在标记前都不算成功。没有成功标记的 orphan/partial 不清理旧副本。
6. 仅成功发布后，核验旧副本大小、SHA 文件及完整摘要，再保留最近三份有效副本。新成功副本提交时间由远端单调递增排序，避免同秒或时钟倒退影响保留次序。
7. 本次源端临时 dump 在成功/失败退出时回收，不建立另一层长期本地月度副本。P7 原工具保持。

文件为 `rai-YYYY-MM-DD-HHMMSS-<随机UUID>.dump`（文件名时间为 UTC），配对 `.dump.sha256` 和 `.dump.json`；用户配置时区和 UTC 快照起始时间另存 metadata。UUID 防止同秒人工重试碰撞。`restore_tested=false` 始终保持，真实恢复演练另存证据，不能靠 list 冒充验证。

远端目录逐级 `openat/O_NOFOLLOW`，末级必须为账户所有的 0700 目录；普通文件要求 0600、单硬链接、当前账户所有。目录标记 `.rai-monthly-backup-v1` 只能在空目录初始化；拒绝接管已有其他文件的目录。不跟随目录、文件或锁的 symlink。不运行通配符删除，也不删除未知文件、partial 或损坏副本。损坏副本会在 check 中失败并保留供人工调查，因此异常目录可能多于三个文件组；保证的是**最近三份已核验成功备份**。

断网、SSH失败、超时、dump/归档检查失败、远端磁盘满、短上传或SHA不符在成功manifest发布前发生：非零退出，不触发保留清理。若成功标记已落盘后清理/响应或本地日志持久化失败，结果可能是“已发布但未确认”，远端可能已经完成三份保留；先执行 `check/list` 核对，不能声称所有旧文件仍在。进程SIGKILL可能遗留本次私有临时文件，需操作者确认没有运行任务后手动处理；partial不会被计为云端成功备份。

SHA 用于完整性检测，不提供文件加密或独立数字签名。没有 age/GPG/密钥管理新流程。既有 P7 文件加密入口仍可显式使用，但本月度入口不调用它，也不同时安装两套计划任务。

`pg_dump` 提供单库一致性导出，角色、角色密码、证书及外部 secrets 不在归档中；custom 格式支持 pg_restore。依据 [PostgreSQL 18 pg_dump 文档](https://www.postgresql.org/docs/18/app-pgdump.html)。本任务接受约一个月的服务端数据损失窗口，不使用 PITR；错过一次计划或离机失败会使窗口更长。小合成库演练不能证明生产 RTO。

## 唯一的定时任务

只准备 [user crontab 模板](../deploy/release/monthly-backup.crontab.example)，**本轮没有安装**。

用户批准安装时：先核对宿主 cron 日历时区和 `RAI_BACKUP_TIMEZONE` 一致，核对已有任务避免重复；填入真实绝对路径、受限日志文件、完整 release 环境。cron 的 `TZ=` 通常只影响子进程，不能用它声称改变 cron 日历。`rai-backup-scheduled.sh` 还检查配置时区下确为每月 1 日 03:00，错时返回 75 并记录原因。宿主时区改变后必须重新核验日历。

不做自动补偿。宿主当时关机、cron 未运行或窗口错过时，用户检查 cron 日志并运行手动 `backup`；手动命令没有月度时间窗口限制。没有额外 daemon、scheduler、监控平台或常驻 Go 服务。

## 月度检查与旧监控适配

`check` 以上海日历每月1日03:00为计划点，宽限24小时；到2日03:00起，必须存在快照起点不早于本月计划点的已核验成功副本。宽限内仍要求满足上月计划，不能因宽限掩盖前一个月漏备份。迟到上传旧 dump 不算本月恢复点。逐月计算日期，既不使用26小时，也不使用固定30天。

| health.status | 含义/动作 |
|---|---|
| `NO_BACKUP` | 受管目录没有成功manifest；调查首次备份是否完成 |
| `BACKUP_OK` | SHA完整且满足月度日历；JSON另含下一计划时间 |
| `BACKUP_OVERDUE` | 超过宽限仍未满足应执行计划；手动运行backup |
| `BACKUP_FAILED` | 本机最近一次备份失败且没有更新的成功快照；检查脱敏错误码后手动重试 |
| `BACKUP_CORRUPTED` | 已发布三件套不完整/摘要不符/时间证据不合法；保留文件调查 |
| `REMOTE_UNREACHABLE` | SSH或传输超时；检查网络、固定host key与身份 |

每次操作另输出JSON日志：UTC时间、operation、status、duration_seconds、backup_filename、backup_size、error_code；备份还含完成时间/SHA/真实PG版本/schema。本地私有状态目录原子保存0600的 `last-backup-attempt.json`；失败不改远端成功manifest。新的成功备份解除失败标记。没有可写本地状态时，只能依赖进程错误日志；远端目录权限/解释器错误可能归为 `BACKUP_FAILED`，不伪装为网络故障。只有本机发起的尝试受此记录覆盖，崩溃/SIGKILL后的未确认操作须人工检查。

已显式适配原P7宿主监控（加载同一受限环境后）：

```bash
/ABSOLUTE/rai/scripts/p7-monitor.sh --monthly
```

该入口现场调用月度check，再聚合P7原有API、Worker、TLS、磁盘和队列规则；月度告警不使用26h。`p7-alerts.py --backup-policy monthly --backup-metadata CHECK_RESULT.json ...`只接受10分钟内的check证据；不能直接塞单个dump manifest。无效/过旧证据退出2，actionable告警退出1。不发送通知、不恢复数据库、不自动补偿。历史 `p7-monitor.sh DAILY_METADATA` 保持显式每日工具兼容（26h）；月度部署只使用 `--monthly`，不能同时启用旧每日监控入口。

## fnOS 正式安装前的命令与批准点

目前缺少正式V2 project/卷/数据库身份、安装目录、release环境及云端SSH host/port/user/key/专用目录。本机通用 `cloud`/`cloud-u` 别名不能证明它们是备份账户；用户也未确认配置。不得按旧系统目录猜测。以下命令是填入已审核配置后的操作清单，**尚未执行**。

1. 先部署审核后的V2（独立发布任务），核对 `docker compose ... ps`、postgres容器完整ID、project/service label、`rai_v2`、`rai_backup_login`、schema16、PG18及明确卷；不得将旧 `rai` 或 `rai-p7-test-*` 标为正式来源。只复制已审核脚本和模板到新的绝对安装目录，不覆盖旧项目。
2. 把示例与已有release配置合并到仓库外 `monthly.env`，父目录0700、文件0600；同账户私钥0600，known_hosts来自可信指纹核对。配置中不得使用测试凭证或UNRECORDED源版本冒充发布版本。
3. 填好配置后，仅做只读SSH探针（先人工核对目标身份，不运行backup来测试连接）：

```bash
set -a
source /ABSOLUTE/private/monthly.env
set +a
ssh -T -F /dev/null -o BatchMode=yes -o IdentitiesOnly=yes \
  -o StrictHostKeyChecking=yes -o ForwardAgent=no -o ClearAllForwardings=yes \
  -o ConnectTimeout=10 -o "UserKnownHostsFile=$RAI_BACKUP_KNOWN_HOSTS" \
  -p "$RAI_BACKUP_SSH_PORT" -i "$RAI_BACKUP_SSH_KEY" \
  "$RAI_BACKUP_SSH_USER@$RAI_BACKUP_SSH_HOST" 'id -un; python3 --version; df -Pk'
```

此示例要求已填显式known_hosts文件。还须只读 `stat` 检查批准的父目录/目标目录、属主、权限、非symlink、非Web根目录、与其他项目隔离，以及空间足够新dump＋旧三份＋临时副本；未知目录归属时停止。SHA不是加密或数字签名，云服务器管理员可以读取明文备份。

4. **PENDING USER APPROVAL**：明确批准创建目标末级目录、首次真实上传，以及本工具在第4份成功后删除最旧受管副本。首次 `rai-backup.sh backup` 会自行初始化空末级目录并创建manifest；不需要额外云端安装包。随后运行check/list，保存脱敏开始/结束/大小/SHA/PG/schema/归档目录证据。失败时保持原副本。
5. 经批准取回刚生成的三件套，严格SSH下载到本地0700目录、文件0600。按下节恢复到全新隔离PG18，逐表与不变量/原回执的只读归档验证。**只有真实上传→真实下载→新PG实际恢复→核验全部成立，才能写REAL CLOUD BACKUP RESTORE: PASS。** 当前未执行。

下载命令（环境同上；`backup_file`逐字取自list的已选受控文件名，`download_dir`必须为本次新建私有目录）：

```bash
backup_file='rai-YYYY-MM-DD-HHMMSS-32_HEX_UUID.dump'
download_dir=/ABSOLUTE/private/new-cloud-restore
# 填入真实受控文件名；三个文件逐一下载，无远端通配符或删除。
umask 077
mkdir -m 700 "$download_dir"
for suffix in '' .sha256 .json; do
  scp -F /dev/null -P "$RAI_BACKUP_SSH_PORT" -i "$RAI_BACKUP_SSH_KEY" \
    -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes \
    -o ForwardAgent=no -o "UserKnownHostsFile=$RAI_BACKUP_KNOWN_HOSTS" \
    "$RAI_BACKUP_SSH_USER@$RAI_BACKUP_SSH_HOST:$RAI_BACKUP_REMOTE_DIR/$backup_file$suffix" \
    "$download_dir/"
done
chmod 600 "$download_dir/$backup_file" "$download_dir/$backup_file.sha256" "$download_dir/$backup_file.json"
```

取回不改云端；实际真实数据取回仍须用户批准。SHA/metadata/归档检查由下节restore工具执行；之后还必须实际SQL与应用核验。

6. 链路通过后，准备一个用户crontab条目并交用户明确批准。先保存旧crontab为0600文件，人工合并模板（保留其他任务），确认仅一个RAI备份条目。经批准执行 `crontab /ABSOLUTE/private/reviewed-user.crontab`，再只读 `crontab -l`、`systemctl is-active cron`、`timedatectl show -p Timezone --value` 验证。日志文件预建0600，沿用既有日志管理，不增加服务。**本轮未安装。**

本次日期下预期下一计划为 **2026-11-01 03:00:00 +08:00**；实际安装时重新计算。受控日历测试不是一次真实月度触发。错时包装器、SSH失败、备份过期均需用户手动backup，不自动恢复、不修改其他任务。crontab没有“next run”标准输出，须把已安装的唯一表达式、cron实际时区与日历计算共同核对；不能仅看模板确认调度成功。

排障顺序：缺配置先补配置；SSH失败先查可信host key/账户/密钥/网络；`unsupported_source_*`查正式源归属与版本；损坏先保留证据而不是删除；备份失败后手动重跑并check。不要把锁忙当成功，不自动清理未知partial或其他项目文件。

## 手动取回与隔离恢复

真实云端取回需要另行授权。取回选定的 dump、SHA 和 JSON 三个文件到私有 0700 本地目录，设为 0600，核对受控文件名和来源。不要恢复来历不明归档。

1. 保持正式 API/Worker 停止、正式入口隔离；先盘点源备份、身份、schema、凭证撤销状态和目标。不能覆盖在线数据库。
2. 按 P7 部署配置创建**另一隔离 project、空卷和全新数据库登录密码**，PG18 空 `rai_v2`，bootstrap 创建 `rai_owner`；不先执行业务 migration。隔离目标 PostgreSQL 容器明确设置 `rai.restore-target=true`。
3. 检查目标 API/Worker 没有运行，数据库没有其他 client backend；抄录 Docker inspect 得到的完整容器 ID，显式传入：

```bash
/ABSOLUTE/rai/scripts/rai-restore.sh /ABSOLUTE/private/rai-....dump \
  EXACT_ISOLATED_CONTAINER --confirm-target FULL_INSPECTED_CONTAINER_ID
```

4. 工具检查备份格式、来源库 `rai_v2`、schema16、PG18、文件权限/大小/SHA、容器 label/完整身份、目标 project API/Worker、数据库 owner/版本、连接数，以及 public 中没有任何 relation/function。拒绝未确认、非空或使用中的目标；不提供 `--clean`、DROP 或正式库覆盖模式。
5. 单事务 `pg_restore --role=rai_owner --single-transaction --exit-on-error --no-owner --no-acl`。保持无 API/Worker 入口隔离；脚本设置默认只读，归档无需安装 runtime 写权限。检查 schema 与备份 manifest 一致（当前新库为19）、validated FK、原回执/stream/屏障、P4 publication、P5 Effective/Override/Execution/Feedback；不启动 API 做 readiness。`rai ops verify` 全表摘要仅在显式维护验收运行。
6. **SQL 恢复不是 HealthKit 灾难恢复完成。** Phase 5B 正式恢复采用新的 Dataset / Session，参照 [Disaster Recovery](RAI_DISASTER_RECOVERY.md) 两条 runbook；恢复旧库仅为 QUARANTINE / ARCHIVE，不接旧 Session，不清空手机状态。正式恢复/入口切换必须由用户另行批准；本工具本轮只验收隔离演练。

运行前检查只能证明检查时刻安静，不能替代网络隔离和操作者维持停机。数据库角色由 P7 bootstrap/授权脚本重建，设备 token 摘要来自旧备份，可能恢复已撤销凭证；即使核对撤销/轮换，也不得将旧备份切成 active continuation；active 恢复需要新 Dataset/Session。
