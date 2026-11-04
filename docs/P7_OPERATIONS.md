# P7 Operations

本文只描述已实现命令与保守策略。实际门禁见 [P7 Result](V2_PHASE7_RESULT.md)。正式环境尚未创建/部署；不要对未知项目执行down -v/prune。命令从仓库根执行，明确配置项目、镜像、secret目录与证书hostname；`dc` 可定义为 `dc(){ docker compose -f deploy/release/compose.yaml "$@"; }`。

## 启动、停止、检查

新空库严格按 [Deployment](P7_DEPLOYMENT.md) 完成bootstrap→显式migration13→授权→API ready→Worker。日常只 `dc up -d --wait api worker`，不自动migrate/init。停机先 `dc stop worker api`，后 `dc stop postgres`；原事实与卷保留。Docker unhealthy不会自行触发restart，持续故障应先诊断原因。

```bash
dc ps
dc exec -T api /rai probe           # TLS liveness：进程提供服务
dc exec -T api /rai probe --ready   # TLS readiness：DB/schema16
dc exec -T worker /rai worker-ready # 进程容器状态另查；此处DB/schema/role
dc run --rm --no-deps monitor       # 聚合business health，不含个人健康测量
dc exec -T api /rai version
dc logs --tail 100 api worker postgres
```

日志有界10MiB×3/服务。分享前仍须脱敏；不输出环境变量、secret文件、raw表、请求正文、GPS或心率值。HTTP `request_hash`（`X-Request-ID` UTF-8 字节的 SHA-256 hex）用于关联；operation是规范路由模板，无原始路径/query。durable_commit只表示事务已耐久；响应丢失从原身份恢复，不新建batch。Worker claim、processing/publication duration、稳定error_code用于定位；B4 起普通日志只记录 `scope_hash` / `job_hash` / `run_hash`，训练的 plan/base/effective identity 也只记录 SHA-256 hash；不输出完整 UUID。HTTP 回应的 `X-Request-ID` 和业务回执身份保持原样，聚合指标不返回这些 ID。

## 监控与告警

`rai ops snapshot` 仅显式CLI查询，输出数据库/表大小、估计行数/dead tuples/last_autovacuum、active connections/lock waits/rollback/deadlock累计计数、各状态jobs/expired leases/retries/oldest due age、generation lag、state publication age、analysis/training run状态、training backlog/feedback pending。没有公开metrics endpoint或新的常驻监控栈。请求错误与override冲突由HTTP409等规范路由日志观察；磁盘余量、TLS有效期、Worker进程、备份时间由宿主探针补充。rollback累计值本身不应作为错误率，要看两个采样的差值。

```bash
scripts/p7-monitor.sh /absolute/last-successful-backup/backup.json
```

输出聚合snapshot及alerts JSON；退出0健康、1有actionable告警、2证据缺失/格式错误。无法采集命令非零退出也必须被外部任务当作监控失败。建议用户批准后由fnOS计划任务每5分钟调用；目前**未安装schedule、未接通知渠道**，没有发送邮件或消息。下列规则由合成positive/negative control验证，不能等同真实通知交付。

后续月度专项适配（保留P7历史每日口径）：当前月度方案加载其受限环境后使用 `scripts/p7-monitor.sh --monthly`，调用月度check并使用每月1日03:00上海时间＋24h宽限，不套用下表的26h。`p7-monitor.sh DAILY_METADATA`仍是显式旧每日入口。两者不能同时配置为月度备份的监控；跨云全SHA检查宜低频/手动，不能照搬每5分钟扫描大归档。具体状态、未安装门禁见 [月度备份](RAI_MONTHLY_BACKUP.md#月度检查与旧监控适配) 和 [本轮结果](RAI_MONTHLY_BACKUP_DEPLOY_RESULT.md)。

| 条件 | 初始阈值/动作 |
|---|---|
| API不ready或Worker停止/DB探针失败 | 立即检查进程、角色、PG；不要盲目重启 |
| TLS即将失效或无效 | 小于7天，按证书流程更新；验证链/hostname |
| 最近备份失败或过旧 | 成功metadata不存在/无效或超过26h；检查dump/加密/独立副本 |
| 数据盘不足 | 可用<10GiB；先停止新写入并定位，不删事实 |
| 尚未恢复的job失败/repair_required | failed_jobs_needing_action>0，按error_code处理；repair_required始终告警 |
| lease过期 | 任一；观察下个15s恢复周期，持续则查Worker/数据库 |
| due backlog滞留/analysis generation lag | oldest due>300s；lag>0且backlog滞留则定位依赖 |
| 连续同步服务端错误 | 5分钟≥5个5xx，保留原outbox身份重试 |
| 监控证据过旧 | snapshot>10分钟或明显未来时间，报监控失败 |

feedback pending可以是合法的Workout尚未同步，不自动判失败；publication age需结合活动/dirty状态解释。个人系统不把无新运动当作故障。生产阈值仍需观察真实、授权工作负载后校准。

`jobs`始终保留所有状态计数。旧failed只有在同scope存在更高generation、clean_generation已覆盖其target后，才计入`historical_failed_jobs`并解除当前故障告警；仅排队新generation不能解除。当前代际失败或没有scope证明仍告警。此分类不修改job/run/attempt历史；真实PG测试验证失败→新代际待完成仍告警→实际完成后恢复。故障演练发现旧失败即使业务恢复仍触发永久告警，因此增加此分类；没有删除失败记录或把失败伪装成成功。

## 排障与恢复

- DB不可用：liveness可200、readiness503；检查PG最终TCP启动、volume和空间、schema16、SCRAM和runtime身份。恢复连接后pgx重连，使用原batch/route/command receipt。禁止reset stream或推进anchor“修复”。
- Worker卡住：检查running lease、attempt/error_code及backlog；正常重启后等待原60s lease +15s recovery poll +5s retry backoff。旧token不能发布；不手工改succeeded/clean。invalid input/unsupported version需修正对应实现/参数，不能把raw改成可分析。稳定失败的受控retry流程尚无通用CLI，先调查，不写无审查SQL强制重试。
- 磁盘满：先限制新写入/停止API和Worker，保留PG卷；检查日志与已验证的备份副本，不删除PG/WAL文件，不直接删除健康表。扩容/释放经确认可移除的运维文件后检查PG WAL恢复、ready、原回执与jobs。只有隔离256MiB tmpfs允许本轮注入ENOSPC，绝不填满fnOS系统盘。
- 半发布疑虑：`dc run --rm --no-deps admin ops verify` 为显式离线核验，不放在每次同步或普通health；它会扫描全库并计算hash，生产大库要在授权维护窗口评估，不能高频调用。发现违反不变量立即停止发布，保留备份，走调查/隔离恢复。
- 备份/恢复：[专用Runbook](P7_BACKUP_RESTORE.md)。PG可启动、dump存在或pg_restore --list都不代表应用数据已恢复。
- 凭证/证书：[Security](P7_SECURITY.md)；原子替换file secret后force-recreate相应容器，确认实际新挂载。不要输出真实token或撤销未确认的device。

## Retention inventory 与安全门槛

`rai ops retention-dry-run` 输出精确表计数、schema与 `automatic_deletion=false`；不做DELETE。它是管理盘点，不进入同步热路径。分类如下；“长期保留”不是增长已无限可控的承诺。

| 分类 | 表/数据 | 当前策略与原因 |
|---|---|---|
| 原始事实 | health_samples/workouts、statistics/events/activities、workout_samples、workout_enrichments；sealed routes/chunks/points、series/chunks、已封Workout package | 长期保留；明确来源删除仍按P3事实事务执行，不按年龄自动删 |
| 永久身份/屏障/恢复身份 | datasets/devices/collector_leases、streams/batches/pages、entity_tombstones、route_deletions、entity_changes/change_events；training_commands receipts | 长期保留；旧UUID不能复活，原batch/command必须可恢复；无正式replay/identity horizon，不GC |
| 队列与租约 | dirty_scopes/jobs/job_attempts | 不删active/live lease或被receipt/run引用的历史；terminal也先保留。RETENTION DEFERRED |
| 版本化派生历史 | analysis参数/runs/results/publications；training参数/plans/configs/sessions/generation runs/base/effective/overrides/executions/feedback | 长期保留；current pointers、可追溯算法与人工训练历史不能当缓存清理 |
| 临时staging | pending route/series/workout package、abandoned registrations | 不按时间清理；缺乏永久replay结束证明，清理会改变重试/stream身份。RETENTION DEFERRED |
| 可恢复运维状态 | sync_request_budgets/sync_upload_slots | 现有有界slot/过期续用；不新增并发unsafe的行删除 |
| 客户端已交付文件 | 新完成page的payload/transfer result/receipt缓存 | 按下述mark→verify→sweep；无全目录自动删除 |
| 客户端未知/历史缓存 | orphan、取消页文件、升级前遗留receipt、Workout/关联anchor/tombstones | 保留、盘点策略延期；不推断安全删除 |

服务端自动history压缩与staging清理明确 **RETENTION DEFERRED**。容量/告警先暴露增长，未来增加正式身份/重放边界证明后再实现，不能用简单created_at cutoff。

## iOS outbox GC

仅在page全部operations已经严格校验ACK（普通批次包括最后GET sealed page/manifest；route包括seal）后，**同一次state.json原子替换**保存delivered anchor、移除pending page、移除该页本地receipt缓存并标记可回收文件。v1旧state通过optional reclaimFiles兼容；不改batch/seq/anchor/回执协议。服务端原回执不被删除。

GC默认dry-run；实际前台同步结束最多sweep128个marked entries，硬上限512，不枚举全目录。先核验不在任何pending引用中、路径合法、不是state.json/目录/symlink，再unlink，最后持久化剩余队列。marked文件已经缺失可幂等重扫。完成页的transfer身份hash文件也被标记；晚到OS回调可能留下orphan，继续保留。

失败状态写入前不能sweep；写入后kill可以留下垃圾但不能丢未交付事实；unlink后队列写失败，下次幂等再扫。缺少仍被引用的payload时保留page/candidate和错误，不推进anchor，不重新生成上传身份。新合成测试覆盖bad ACK、state写失败、dry-run、有界扫、missing reference、重复调用、state/引用保护、原身份恢复；macOS子进程在GC批次间实际SIGKILL并重开验证。该证据不是iPhone force-quit或后台URLSession系统验收。

Product command journal、HealthKit Workout引用/anchor和永久本地删除屏障未清理；训练/同步UI、Liquid Glass和WorkoutKit无新增页面或功能。P3/P6真实设备门禁继续NOT VERIFIED。
