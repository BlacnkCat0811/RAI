# M7 evidence — synthetic only

结论与边界以 [M7_RESULT](../../M7_RESULT.md) 和 [validation.json](validation.json) 为准。只使用 synthetic fixtures；没有生产数据库、真实健康数据、凭证、promotion 或部署。

| Evidence | 实际执行范围 |
| --- | --- |
| baseline.json / frozen-check.json | M7四项Git基线；79文件原方案/模型/migrations/App冻结SHA |
| pg-full-race.txt | 新建隔离PG18.6，`go test -race -count=1 -timeout=40m -tags=integration ./...`，PASS；integration1018.755s |
| pg-final.txt | 最终M7 reader/DTO专项PG18.6/race，含latest-pending、missing dependency、actual DAG、corruption、真实roles、assignment/revision/pointer race、privacy、4类性能观测 |
| go-test.txt / go-race.txt | 最终全Go编译/测试回归 |
| go-vet.txt / integration-vet.txt | 最终vet；空stdout来自成功exit0，不以空文件存在当通过 |
| boundaries.txt / sqlc.json / diff-check.txt | 真实依赖边界；sqlc1.31.1再生成hash一致；Git whitespace check |
| contract-validation.txt | Draft2020-12校验11golden；Swift fixture同字节；旧OpenAPI paths/schema不变 |
| openapi-baseline.txt / openapi.txt | Redocly2.71.1：旧31warnings；当前0errors/33warnings，新增2为internal schemas unused |
| swift-full.txt | 完整Xcode命令级DEVELOPER_DIR；132tests/17suites PASS；Daily/Workout/SyncCore+DTO |
| simulator-summary.json / simulator.txt | 正式App XCTest41PASS/2专用live-host测试skip；M7 golden明确Passed |
| simulator-live-regression.txt | 被常规runner跳过的两项由专用host实际重跑：B4 groups9+8cases及B2 lost-ACK App/HTTP/PG18/Worker均PASS |
| toolchain.txt | Xcode26.6/Swift6.3.3/SimulatorSDK26.5/runtime26.5实测 |
| benchmarks.txt / performance.json | 纯Go重复3次benchmark；真实4场景DBReads/Freshen/map/JSON/bytes/reader+JSON allocations |
| final-source-sha256.json / manifest.json | 最终实现与证据SHA；真实已完成命令状态在validation中 |
| privacy-scan.json | 新证据full UUID扫描；无凭证或raw health读取 |

全量PG回归与之后的最终reader校验改动是不同编译快照，最终改动再通过Go all/race/vet与M7专项PG/race；不把旧full-run声称为包含之后所有修改。早期调试输出不作为最终验收依据。所有作为最终通过证据的fresh cluster都有leftover databases=0 / cluster removed=1；专用Simulator结果保留在`.local`的Xcode xcresult。

可复验命令（只选择synthetic Simulator；无全局xcode-select切换）：

```bash
source scripts/local/lib.sh
rai_go_env
PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -timeout=40m -tags=integration ./...
PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -tags=integration ./integration -run '^TestAthleteModel' -v
DEVELOPER_DIR=/Users/ta/Downloads/Xcode.app/Contents/Developer swift test --package-path ios/RAI
# Set RAI_M7_SIMULATOR to an explicit synthetic iOS Simulator before this command:
DEVELOPER_DIR=/Users/ta/Downloads/Xcode.app/Contents/Developer bash scripts/test-m7-simulator.sh
```

合同验证工具安装在忽略的`.local/m7-validation`，不更改后端/客户端runtime依赖：jsonschema4.23.0、PyYAML6.0.2；实际安装日志为contract-tools.txt。运行`.local/m7-validation/bin/python scripts/check-m7-contract.py`。OpenAPI固定工具用`scripts/local/contracts.sh`。Go固定工具链1.27.3，sqlc1.31.1。

**REAL DEVICE ACCEPTANCE: DEFERRED / PHYSIOLOGICAL VALIDATION: NOT VERIFIED / PRODUCT ACTIVATION: NOT AUTHORIZED**。Simulator/SQL/mock/文件存在均不代替真机、生理、部署或产品activation验收。
