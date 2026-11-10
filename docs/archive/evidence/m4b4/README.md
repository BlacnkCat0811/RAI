# M4-B4 machine evidence

仅保存 synthetic count/status/hash/timing 与命令终态；无健康 payload、SQL/assertion 正文、source descriptors 或凭证。

- `baseline.json`：执行前真实 dirty Git baseline 与起始文件哈希，B3 改动保留。
- `validation.json`：独立 Go / PG18 / real-LOGIN / Simulator / client recovery / Swift 门禁，明确 opt-in skip 与编译快照范围。
- `*-events.json`：Go terminal events（测试身份为 hash），不保存 Output。
- `*-observations.json`：whitelist 提取的 count/status/timing。组件 synthetic ACK 与完整 HTTP/PG/Worker E2E 分列。
- `freeze-scan.json` / `sqlc.json`：冻结文件与 sqlc1.31.1 两次复现。
- `manifest.json`：其余证据文件 SHA-256。

重跑正式 Simulator：显式设置 `DEVELOPER_DIR`、`RAI_M4B4_SIMULATOR` 后执行 `scripts/test-m4b4-simulator.sh`。完整 PG 回归使用 `scripts/test-postgres.sh go test -race -count=1 -tags=integration -json ./...`，该 runner 始终创建新 PG18 cluster，不使用已有数据库。最终 evidence 整理执行 `python3 scripts/collect-m4b4-evidence.py`，输入为 ignored `.local/m4b4` 私有日志；缺终态、失败或缺清理时拒绝标绿。

REAL DEVICE ACCEPTANCE: DEFERRED。PHYSIOLOGICAL VALIDATION: NOT VERIFIED。RAI OWN VO2MAX MODEL: NOT IMPLEMENTED。
