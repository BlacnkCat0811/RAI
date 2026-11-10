# ADR-001：在当前目录从零实施 Go / PostgreSQL 架构

状态：已接受，2026-10-27（Asia/Shanghai），依据用户本次明确要求。

## 实施边界

当前工程根为 `/Users/ta/rai`。同目录的 [方案](../../RAI-Go-PostgreSQL架构重设计与实施方案.md) 是目标蓝图，保留原文；其中旧项目路径和“本次只交付文档”是原方案编写时的事实，本次已授权在当前目录开始实现。

`/Users/ta/running` 只作为后续语义迁移的参考，不修改、复制或部署其工作树，不读取真实健康数据库。当前工程不继承旧服务作为运行依赖，不将新 Go 服务自动接入旧生产流量。

保留单用户、一个主要采集手机、原生 iOS、只读 HealthKit、WorkoutKit 单独排程授权；不创建 Web、独立 Watch 或桌面客户端。后端为 Go 模块化单体、一个 PostgreSQL 数据库，API 和 Worker 同一镜像、不同进程；不引入常驻消息中间件。

## 首个可验收切片

先实现类型白名单内的数量／睡眠类别样本上传，建立事实、tombstone、批次回执、流连续序号、提交版本、变更日志与分析待办的同事务边界。拒绝未实现的 Workout、路线、series 和未知字段，不静默裁剪载荷。

所有业务事实写事务先锁 dataset，再锁 stream；提交版本来自 dataset 行更新，不用 sequence 充当提交顺序。无业务变化的批次仍提交回执和流位置，不增加业务版本。

未知删除仍产生永久精简屏障；同批 delete 优先，同 UUID 不同内容拒绝。范围已知的变更创建按实体定位的脏范围和任务。此时尚未实现跨指标影响规划，不能把这些待办当成算法已完成。

最终回执按原始 JSON 字节以 `bytea` 保存，GET 和重复 PUT 返回同一回执内容。`jsonb` 仍用于 envelope、metadata 和变更投影。这样可以直接验证提交后响应丢失的恢复。

任务完成保留 scope 的递增 generation，并独立记录 `clean_generation`，防止清理 scope 后复用历史任务身份。Worker 的每次续租和完成都必须检查有效租约、running 状态及 fencing token；不能清掉期间新增的 generation。

## 工具链与权限

初始固定 Go 1.27.3、PostgreSQL 18.6、pgx/v5 5.11.0、sqlc 1.31.1、goose 3.29.0；Go/PG 容器摘要随源码固定。选择依据是本次核对的官方发布信息；版本升级另行验证。

API 使用 pgx 和 sqlc 生成的显式查询；事务内的写 SQL 由 repository 集中实现。migration 仅管理命令可以触达，在线 import graph 门禁检查实际依赖。API 权限组不能创建 schema、改设备身份、读服务器文件或调用备份；Worker 使用独立权限组。开发数据库所有者只用于隔离开发与测试，见 [角色脚本](../../deploy/postgres/roles.sql)。

设备凭证由显式 `init` 管理命令产生，数据库只存 SHA-256。禁止在运行日志中输出健康值、完整请求、token 或含凭证的 DSN。

## 回滚和后续

当前不存在生产切换；新增工程可停止运行而不影响旧服务。已持久保存的新事实不得通过 Down migration 删除；后续优先兼容同库代码回退或向前修复。

旧数据策略已由 [ADR-002](002-fresh-healthkit-dataset.md) 更新：本次不上旧库迁移链路，发布后由手机在新数据集的同步命名空间首次导入可见 HealthKit 历史。新库接收真实写入后，不能直接把入口切回落后的 SQLite。

现场硬件、备份位置、维护窗口、RPO/RTO 和主要手机接续状态尚未核实，不据此编造部署参数或发布承诺。阶段证据和未完成项统一记录在 [实施记录](../IMPLEMENTATION.md)。
