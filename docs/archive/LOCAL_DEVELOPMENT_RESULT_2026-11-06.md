# 本机开发提速与独立真机环境（2026-11-06）

本轮执行地点为本机 Mac，未调用 N100、SSH 测试入口或远端数据库。基线 `main / 533fa82`，修改未提交。用户明确要求全部测试在本机，并授权同时建立新的本机真机验收环境；原云端验收数据和手机命名空间不迁移、不清空。原方案、迁移、协议和历史验收报告原文不变。

## 日常反馈与完成门禁

- `scripts/local/test.sh quick`：Go unit/vet/依赖边界及本机控制器安全测试；最后实测 **7 秒**，复用缓存。
- `scripts/local/test.sh gate`：Go race/vet/依赖边界及控制器安全测试；实测 **19 秒**。事务变更必须另跑受影响的真实隔离 PG 测试。
- `scripts/local/test.sh unit -run ... ./internal/syncing`、`integration -run ... ./integration`、`swift --filter ...`：按修改范围验证。
- `scripts/local/test.sh full`：需要全量回归时，运行 uncached race、vet、build、sqlc 和完整隔离 PG18；本轮结果见下方最终门禁。
- `scripts/local/test.sh simulator`：完整 synthetic App XCTest；`e2e` 仅构建并执行专项 App→本机 HTTP→本机 PG18，不再先重复整套 App 测试。

`quick` 不是完成门禁，合成规模测试不是真机验收。完整真机历史补齐保留为单独阶段任务，不再作为每次修改的反馈循环。旧 `test-remote.sh`、`remote-test.sh` 入口在连接任何主机前直接拒绝执行；AGENTS.md 已记录本机优先约定。现有 GitHub CI 保留，未触发 push。

## 已修复的客户端复杂度问题

`associateDiscoveredFacts` 原先对每个待关联 sample 扫描全部 discovery events，成本为 `K × E`；即使所有事件已经有关联，也会重复扫描。现在仅一次建立未绑定事件索引，再按原 keys 顺序处理对应事件，成本为 `E + K + 匹配事件数`。保留重复 key 幂等、同一事实的多个 revision/delete、generation、父绑定冲突和最终原子状态提交；新增重启和部分冲突回滚回归。

同一组合成输入（10,000 discovery events / 1,000 association keys，包含最终原子状态编码/落盘）在本机 Swift Debug 下，旧实现 **9,328.407 ms**，新实现 **124.222 ms**，约 **75 倍**。这是本机单步骤测量，不宣称 iPhone 整体快 75 倍。

逐点 series/route synchronize、fresh series 查询和全状态原子保存仍保留；未以关闭同步落盘、跳过数据或提前 anchor 换速度。它们仍可能是大历史场景的耗时项，未在本轮完成逐项真机归因。

## 本机规模测试

真实本机 loopback HTTP→生产 handler→隔离 PostgreSQL 18.6 durable COMMIT，1/10/100/500 条独立 Workout 均核对事实行数与同字节重试原回执。500 条提交 **20.955 秒**，HTTP 中位 **34.870 ms**、P95 **62.931 ms**。

每条仅含 60 个 scalar samples、3 个 series points 和 3 个 route points；Worker 未参与性能测量。基准显式采用有限的 upload budget（burst 1024、60000/minute），结束恢复原预算；首轮因默认 burst=2 出现 429，未将该轮记为通过，未修改生产默认预算。此结果不是密集长跑、真实 HealthKit 或产品容量承诺。既有 dense performance matrix 保留；completeness v2 单 manifest 256 条 inventory 上限未变，500 次独立 bundle import 不能冒充支持 500 条 manifest。

## 新本机真机链路

USB 实测：iPhone 12 Pro Max、wired、paired、developer mode enabled。已构建、签名并更新安装 Debug App，没有卸载。新 Debug 本机模式在普通 session 初始化前分流，关闭产品轮询，使用独立 `local-acceptance-<namespace>`，不激活或重绑定原 Keychain/session。

独立 PostgreSQL 18.6、schema 29、API/Worker 均在 Mac；数据库仅 loopback，API 绑定 Mac 选定的私有 LAN 接口，握手广告 v2。认证 iPhone→Mac probe **81.933 ms**，本次操作前后的原 sessions manifest SHA 比较一致。USB 用于安装/启动/取证，API 使用私有局域网；Debug HTTP 限定到构建配置的单一主机，Release TLS 规则不变。开发数据库使用独立 owner 角色，不宣称生产最小权限或 TLS 验收。

首条真实 eligible Workout 提交并核验回执：**18.421 秒**；采集/组包累计 **12.050 秒**（其中 quantity/series 读取及捕获 **10.106 秒**），上传/确认 **5.676 秒**。重启后重复核验 **12.652 秒**，使用 committed Workout anchor，并精确复用原 bundle 身份。服务端前后均为 1 Workout / 1 import / 5,464 samples / 1,768 route points，无重复增加。此 single 路径与历史全量 completeness 的查询/状态规模不同，不作同条件端到端提速倍数比较。

控制器支持保留数据库/凭证的 stop→prepare；实际停止后已在同一新本机身份重启。私有目录 `.local/device-acceptance` 为 0700，凭证/日志为 0600；公开报告仅含数量、技术耗时、状态与布尔。控制器安全测试覆盖 host 拒绝、输出最小化、原子写失败保留原状态及外来 run 身份拒绝。

## 本机端到端工具修复及验证

复现并修复既有 opt-in Simulator HTTP/PG fault injector：成功的 v2 COMMIT 后错误地使用严格 v1 decoder，导致钩子持续返回 500，客户端保持 generation 0。现在按实际 contract 解码，断言本次为 v2，并自动保存私有失败诊断。未修改生产 decoder、API 事务或回执语义。历史 R3B 报告保留原文，不把本轮失败/修复回写为历史通过。

修复后专项 **PASS**：两代 v2 manifest、12 streams、1 import、真实丢 ACK 重试、第二轮 bootstrap=0、M6 synthetic=false 且 unknownLoads=1；PG/race package **46.125 秒**，入口含增量构建共 **64 秒**，残留测试库 0、cluster 已删除。默认 admission 下观察到 429 并按合同重试，未关闭限流。

Swift Testing **164 tests / 23 suites PASS**，另有 XCTest 6 项（3 通过、3 显式外部条件跳过，含本轮 opt-in 性能测量）。Simulator App XCTest **60 项：57 通过、0 失败、3 显式外部 fixture 跳过**；R3 HTTP/PG 专项由独立入口实际执行，另两项未冒充本轮通过。

## 最终门禁与未验证项

**LOCAL DEVELOPMENT: PASS WITH RISKS。** Go race 单测、vet（含 integration/ua1rsim）、build、sqlc 一致性和依赖边界通过。聚合证据见 [local-development-2026-11-06.json](evidence/local-development-2026-11-06.json)。

**全量 PG/race 首跑失败，不能记为全绿：** integration 包耗时 **1300.994 秒**，仅 `TestFeatureLoaderLimitsFailWithoutTruncatingOrPublishing` 报错，其他包通过，残留测试库 0、cluster 已删除。该用例独立重跑 **7.33 秒 PASS**，显示其断言对执行负载敏感；首跑没有留下 Worker 最终错误码，不能据此断言具体超时原因。

已把这项 observation 数量边界测试的重复载荷改为先通过生产 `Validate` 的合法 metadata-only observation，避免复制 2001 份与数量边界无关的 sample/route provenance 和 coverage。仍真实插入并断言 2002 行、验证同一个 2000 上限、禁止 artifact/current 发布，并加强断言 job 必须 durable `failed` 且错误码恰为 `analysis_input_limit`。生产 Loader、Worker、SQL 事务、上限和超时完全未改。修复后的真实隔离 PG18/race **连续 5 次通过**（每次约 2.6–2.9 秒，package 14.743 秒），清理检查通过。

**整套 PG 回归未在 fixture 修复后再次执行。** 本轮仅做受影响项复验，不把首跑失败改写为 PASS；全量回归新一轮是否全绿仍未验证。事务实现未变，原 Go 完成门禁与受影响的真实数据库复验已执行；后续需要全量签核时仍必须实际运行 `full`。完整套件在本机仍是较长阶段门禁，本轮提速结论主要来自日常定向反馈和客户端关联算法。

真实完整 v2 inventory/eligible streams、真实 manifest lost-ACK、第二代真实 completeness 和真实 M6 重评本轮**未执行**，数据库 completeness manifests=0。本轮不改变 UA1-R3B 的历史 BLOCKED 结论，不宣称 UA1 重验完成。Docker/Linux/P7 容量和灾难恢复场景、生产角色/TLS/部署、跨人群可用性未执行；不回退 N100。

**PHYSIOLOGICAL VALIDATION: NOT VERIFIED；PRODUCT ACTIVATION: NOT AUTHORIZED。** 无模型公式/资格、产品消费者、原方案、旧 running 或真实数据迁移变更；无 commit/push、生产部署或入口切换。
