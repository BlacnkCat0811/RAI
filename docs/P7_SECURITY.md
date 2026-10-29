# P7 Security Boundaries

基线 `d11e7b29e339b3d33c84962611031a3f33a18a7c`；原P3/P4/P5/P6合同和00001–00013 checksum保留。本轮使用新建随机credentials、独立卷与合成数据。实际结果见 [evidence](evidence/p7-fnos-2026-10-29.json)。

## 入口与身份

唯一产品入口是原生iPhone→HTTPS→Bearer认证API→内部PostgreSQL。发布模板仅绑定宿主回环8443；PG不映射宿主端口、仅internal database网络；Worker无入口/端口。没有自动启用公网、frp、VPN或现有代理。正式手机可达性、DNS、私网/防火墙现场检查仍需用户批准后执行。

Go TLS最低1.2，使用完整证书链/匹配私钥。probe连接回环但仍按 `RAI_TLS_SERVER_NAME` 验hostname、system/明确CA验链；没有InsecureSkipVerify。正式模板 `RAI_REQUIRE_TLS=1`，证书不完整启动失败。私有测试CA只用于隔离演练，不代表真实iPhone已信任。Release Info.plist无ATS全局例外，设备凭证device-only Keychain；WorkoutKit授权仍deferred。

## 数据库权限

| 身份 | 权限与用途 |
|---|---|
| rai_bootstrap | 新专用cluster内管理，超级用户；不挂给应用 |
| rai_owner | 非superuser的DB/schema owner，只用于显式migration/init/维护 |
| rai_api_login → rai_api | 正式Sync和Training必要DML、derived只读、受控Route函数；无schema CREATE/TEMP/DDL/owner membership |
| rai_worker_login → rai_worker | 读取分析输入、claim/renew/requeue、derived与训练发布、dirty/jobs/attempt；无raw UPDATE或migration |
| rai_backup_login | 所有应用表/sequence SELECT，仅用于完整dump |
| rai_monitor_login | 指定队列表/analysis/training状态只读与pg_read_all_stats；不授予raw表读取；敏感管理身份，仅CLI使用 |

`RuntimeRoleSafe` 在发布API/Worker启动及Worker探针拒绝superuser、createdb/createrole/replication/bypassrls、schema CREATE、TEMP或DB owner membership。`migrate`/down显式要求owner（包括无待迁移的情况）；旧业务migration不回写。角色授权在migration之后显式执行，默认PUBLIC函数EXECUTE及public schema CREATE/TEMP撤销。

Route保留SECURITY DEFINER正式函数与固定search_path，API不能直接写chunks/points、sealed state、receipt或删除route；无TEMP/CREATE防止shadowing。真实数据库用实际LOGIN及SQLSTATE42501回归DDL/TEMP/角色切换/Route绕过；Worker与API既有P4/P5受限角色闭环继续回归。API按Bearer对应dataset隔离，撤销后认证拒绝；写事务重新核验collector lease。数据库runtime组可服务多个dataset，当前**没有RLS或针对被攻陷API进程的租户隔离承诺**；其边界是可信API+查询限定，不为P7改写P3。

## 应用安全审查

保留16KiB JSON登记、2MiB wire/8MiB decoded、1000 events、5000 route points/chunk、64KiB line、各自chunk/detail限额；gzip膨胀、unsupported字段/单位拒绝；持久admission和upload lease/fencing不放宽。HTTP header5s/read15s/write25s/idle60s，request context20s，数据库session2s/事务5s/锁500ms；未增加无界缓存或新队列。

错误返回稳定code，SQL/DSN/正文不返回；日志只记录request UUID、规范路由pattern、组件、operation、耗时、状态、稳定error_code与数量。未知URL输出unmatched，不再记录用户控制的原始path或query。durable_commit在COMMIT后、响应写前产生，不能表示手机收到ACK；chunk日志不是route published。Worker日志保留现有操作job/run lineage，记录claim/processing/publication耗时，不记录HR、GPS或payload。PG关闭语句/参数错误日志及DETAIL，保留稳定错误定位；隔离故障测试日志不用于存放健康载荷。

iOS outbox文件名限定末级路径，GC只处理已完成页原子标记的文件，阻止引用中/authority/symlink目录目标；不扫描删除未知文件。未改变HealthKit只读权限、collector身份、TLS校验或UI。备份/秘密目录不进入Docker build context，远端源码同步继续排除secrets/.env/.local/证据与编译缓存。

## 轮换与恢复流程

- DB：停对应写入口/Worker并保留原身份待办；操作者用owner/受控管理身份经stdin/受限文件执行 `ALTER ROLE ... PASSWORD`，禁止密码入argv或SQL日志。生成匹配新DSN、权限0640、原子替换宿主secret；预检后force-recreate对应服务，检查runtime身份/readiness/原回执。旧连接可能存活，验证新登录后受控关闭旧会话；不要中断其他库。此版本无双密码窗口，需短维护窗口。
- API token：先确认正确dataset/device并准备新的受控设备注册/collector交接方案；管理员明确设置目标device.revoked_at后立即测试401，客户端保留outbox，不更换batch UUID。无自助注册/无缝token轮换命令，未对真实设备撤销；不能直接改token摘要并假装手机已切换。
- TLS：外部PKI续期，7日阈值告警；核验链/hostname/有效期/key pair，原子装入secret，再force-recreate API并核验实际新证书；不要trust-all或关闭ATS。备份密钥、证书私钥与secret恢复副本分别离线保管。
- Secret遗失：先判断是否泄漏；泄漏需撤销/轮换，并核对数据访问。仅恢复secrets不等于恢复数据库或手机Keychain；无密钥的加密备份无法恢复。记录权限/版本/时间，禁止把内容附到故障报告。

公开证书续期自动化、真实iPhone trust、凭证现场安装、操作系统/防火墙与离机密钥取回尚未验收。未执行真实凭证读取、轮换或撤销。
