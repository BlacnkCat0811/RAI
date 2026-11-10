# 上传查询预算与本地规模门禁

此文只覆盖 P2 的八条关键点查询及最大页恢复，不是 P2 整体验收或生产容量结论。实际结果见 [实施记录](IMPLEMENTATION.md)。没有增加业务能力或 schema，也没有修改冻结的 migration。

查询源为 [core.sql](../apps/server/queries/core.sql)，调用方为 [store.go](../apps/server/internal/storage/postgres/store.go) 和 [pages.go](../apps/server/internal/storage/postgres/pages.go)。[专项集成测试](../apps/server/integration/scale_test.go) 拦截 sqlc 实际传给 pgx 的 SQL 和参数，用同一条 SQL 执行 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)`；没有另写一份供验收的业务查询。

| sqlc 查询 | 查询范围／键 | 单次返回或存在性探测 | 依赖索引 |
|---|---|---|---|
| Batch | dataset + batch UUID | 至多 1 条批记录 | sync_batches_pkey |
| Page | dataset + page UUID | 至多 1 条页记录 | sync_pages_pkey |
| BatchSequenceOccupied | dataset + stream UUID + clientSeq | 至多 1 个命中 | sync_batches_dataset_id_stream_id_client_seq_key |
| LegacyPageExists | dataset + 历史 envelope pageID，限定无新页关联 | 首个命中即停止 | sync_batches_legacy_page |
| OpenPageExists | dataset + stream UUID，限定未封口页 | 至多 1 个命中 | sync_pages_open |
| TombstoneExists | dataset + sampleType + UUID | 至多 1 个命中 | entity_tombstones_pkey |
| SampleIdentity | dataset + sampleType + UUID | 至多 1 条旧范围／内容摘要 | health_samples_pkey |
| WorkoutIdentity | dataset + Workout UUID | 至多 1 条旧范围／内容摘要 | workouts_pkey |

这些查询没有分页或事实时间窗口，输入都是精确身份键；不存在性结果也不得遍历 dataset 历史。门禁在 1 万和 10 万行两档、命中和未命中两种情况分别检查真实索引、每节点实际处理行数及根节点 shared hit + read blocks 不超过 32。32 blocks 是当前点查询的回归上限，不是磁盘 IOPS 或端到端请求延迟承诺；索引深度仍随数据量增加，不能称为严格常数成本。全批 1000 事件会重复多个点查询，该门禁不意味着整批只读 32 blocks。

大表由测试 owner 直接写入合成占位 fixture，保留真实约束和索引，只用于查询计划；不将这些占位数据冒充经协议上传的完整 HealthKit 事实。每档执行正常 `ANALYZE`，不禁用顺序扫描，不修改在线成本参数或超时。只有独立 fixture 写入连接使用 120 秒 statement timeout，在线连接仍为 2 秒、业务提交 context 仍为 5 秒。实测包含共享缓存命中，不宣称冷盘性能或所有 prepared/generic plan 均已验收。尚未覆盖全部在线 SQL、租约／任务／子表删除计划、多 dataset 分布、大量死元组、百万／千万行及生产统计分布。

最大页用例通过真实 repository 逐个登记并提交 10000 分片：首次插入、随后相同事实 no-op、最终删除；中途与最终片登记后重建连接池，在最终封口处注入异常，检查受影响业务行、最终登记、流和页水位全部回滚，再恢复原批次。校验有序 manifest、原字节批回执、后续页删除屏障与不可变页回执。连接池重建不等于进程崩溃、数据库崩溃或多主机故障；此用例不经过 HTTP 请求预算／上传槽，也不测 gzip 编解码吞吐。小页真实 HTTP 和租约验证仍见既有集成测试及实施记录。整页执行成本随片数增长，只有每片查询数量与本页历史片数无关。

从工程根目录运行（Go 及 PostgreSQL 18 工具已安装）：

```bash
PGBIN=/path/to/postgresql/bin scripts/test-postgres.sh \
  go test -race -count=1 -tags=integration,scale -run '^TestScale' -timeout=10m -json ./integration
```

脚本不使用传入的旧测试连接，始终新建仅 Unix socket 的临时 cluster，测试结束检查遗留数据库、关闭并清理。CI 用独立 PostgreSQL service 运行相同专项测试；只有本地实际运行结果可记作本轮证据，工作流存在不能代替远端执行。
