# M4-B2 synthetic evidence

本目录只保留脱敏汇总、源文件 hash 与验证命令；不保存健康库导出、真实数值、样本 UUID dump、设备名称、来源 bundle、凭证或签名账号。硬件测试仅使用 AppTests 的合成 HK 对象，不是 Apple Watch 数据同步或授权/后台验收。

## Environment and commands

以下命令在仓库根目录的 **bash** 中运行。本机使用工程 `.local/toolchain` Go/PostgreSQL/sqlc 和 `/Users/ta/Downloads/Xcode.app`，不切换全局 xcode-select。PG runner 创建新的 Unix-socket-only PG18 cluster，清除 ambient 数据库配置，结束后移除整个临时 cluster。

```bash
source scripts/local/lib.sh
rai_go_env
export DEVELOPER_DIR=/Users/ta/Downloads/Xcode.app/Contents/Developer

(cd apps/server && go test -count=1 -json ./...)
(cd apps/server && go test -race -count=1 -json ./...)
(cd apps/server && go vet ./...)
(cd apps/server && go vet -tags='integration m4b2client m4b2sim' ./...)
(cd apps/server && go mod verify && ../../.local/bin/sqlc generate)
python3 scripts/check-boundaries.py
git diff --check

PGBIN="$rai_pgbin" scripts/test-postgres.sh \
  go test -race -p 1 -count=1 -timeout=30m -json -tags=integration ./...
PGBIN="$rai_pgbin" scripts/test-postgres.sh \
  go test -race -count=1 -timeout=5m -json -tags=integration \
  ./internal/syncing ./internal/admin ./integration \
  -run 'TestVO2|TestRuntimeSchemaGateMatchesMigrations'

swift test --package-path ios/RAI
swift build --package-path ios/RAI --product VO2Probe
RAI_M4B2_PROBE="$PWD/ios/RAI/.build/debug/VO2Probe" \
  PGBIN="$rai_pgbin" scripts/test-postgres.sh \
  go test -race -count=1 -timeout=5m -tags='integration m4b2client' \
  ./integration -run '^TestVO2Swift' -v
```

构建和 Simulator 验证：

```bash
xcodebuild -project ios/RAI/RAI.xcodeproj -scheme RAI \
  -destination 'generic/platform=iOS Simulator' \
  -derivedDataPath .local/m4b2/derived CODE_SIGNING_ALLOWED=YES \
  CODE_SIGN_IDENTITY=- RAI_DEV_HTTP_HOST=127.0.0.1 build-for-testing
export RAI_M4B2_XCTESTRUN="$PWD/.local/m4b2/derived/Build/Products/RAI_iphonesimulator26.5-arm64-x86_64.xctestrun"
# 用 simctl 选择独立合成 Simulator；此变量不写入 repo evidence。
export RAI_M4B2_SIMULATOR='<SYNTHETIC_SIMULATOR_UUID>'
PGBIN="$rai_pgbin" scripts/test-postgres.sh \
  go test -race -count=1 -timeout=12m -tags='integration m4b2sim' \
  ./integration -run '^TestVO2SimulatorApp' -v
xcodebuild test-without-building -xctestrun "$RAI_M4B2_XCTESTRUN" \
  -destination "platform=iOS Simulator,id=$RAI_M4B2_SIMULATOR" \
  -parallel-testing-enabled NO -only-testing:RAITests \
  -skip-testing:RAITests/NormalizerTests/testVO2SimulatorHTTPPostgresEndToEnd
```

E2E harness 给 App XCTest 注入一次性 synthetic identity，走真实 normalizer/coordinator/BackgroundTransfers/HTTP/PG。临时 xctestrun 的 credential 在私有文件中传递并删除；服务端 durable commit 后实际丢弃一次响应，再检查事实/receipt/dirty 各一条和 Int64 精度。它不将脚本直插表冒充 App E2E。

USB 设备的合成测试使用 `.local/m4b2/device/RAI.xcodeproj` 私有副本：将 projectDirPath / local Swift package path 指向原 `ios/RAI`，scheme 引用指向副本；仅将 bundle ID 前缀换成 `com.rai.v2.m4b2.synthetic`，使用现有开发 Team 签名。未改变仓库正式 App 的 bundle ID，不复用其 sandbox/Outbox/Keychain。首次 profile 缺失使用 Xcode `-allowProvisioningUpdates`，后续复跑使用已生成的 profile。此操作安装独立测试包，未部署服务。

```bash
xcodebuild -project .local/m4b2/device/RAI.xcodeproj -scheme RAI \
  -destination 'platform=iOS,id=<PAIRED_DEVICE_UDID>' \
  -derivedDataPath .local/m4b2/device-derived \
  -only-testing:RAITests/NormalizerTests/testVO2SDKMetadataAndProvenance \
  -only-testing:RAITests/NormalizerTests/testVO2IndependentPendingAndBlockedSDKPage \
  -only-testing:RAITests/NormalizerTests/testVO2SDKNormalizationPerformance \
  -only-testing:RAITests/NormalizerTests/testVO2SDKBootstrapPredicateKeepsOverlappingInterval test
```

SDK fixture 的未保存对象没有真实 sourceRevision；测试显式注入合成 descriptor，非法未来 metadata 也仅在测试内注入，生产代码没有 KVC 修改或 HealthKit save/delete。Predicate 的 SDK 对象 evaluation 证明 overlap 规则；不能声称已在真实历史健康库验证 anchored delete / predicate visibility。

OpenAPI 使用固定 Redocly 2.71.1 / Node 22.23.2。结果 0 error / 31 个既有 warning；不升级 lint、不抑制 warning。性能值仅为本机 synthetic observation；EXPLAIN 仅输出 node/index/count/time。

最终状态、实际计数及 skip 原因见 [validation.json](validation.json)，失败历史见同文件；[完整报告](../../M4B2_RESULT.md) 包含未验证项。重复专项不累加为更多独立通过样本。
