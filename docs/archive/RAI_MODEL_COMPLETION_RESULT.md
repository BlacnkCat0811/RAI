# RAI 运动能力模型补全实施结果

状态日期为 2026-11-07，时区 Asia/Shanghai。基线为 `main / 18eab55` 加既有工作区改动。本轮使用本机隔离 PostgreSQL 18.6、合成数据和已授权的只读真机，保留用户改动及旧迁移。逐项状态、执行命令和历史证据见 [验收矩阵](evidence/model-completion/acceptance.json)，目标仍按 [实施计划](RAI_MODEL_COMPLETION_PLAN.md) 和 [验收标准](RAI_MODEL_COMPLETION_ACCEPTANCE.md) 判断。

## 签核状态

| 签核 | 状态 | 依据 |
| --- | --- | --- |
| 工程功能 | PARTIAL | 日栈、回算、正式 API、研究诊断/maximal 发布和合成切换回滚已接线；Feature v2 合同、完整分阶段故障矩阵和部分详情展示仍未完成。 |
| 性能与资源 | PARTIAL | F04、F07 已按当前代码实测通过；F08/F10 未执行，F09 的旧 1,000 ms S60 目标仍超限。独立 Worker RSS 和原生交互性能未齐。 |
| 数据可用性与体验 | PARTIAL | Simulator schema2 E2E、Swift 与真机 Release 安装通过。真机完整性被一条 `metadata_requires_review` Workout 阻断；VoiceOver/动态字号/窄屏矩阵未齐。 |
| 真机 | FAIL | D01 PASS；D02 因真实 `metadata_requires_review` 无法证明完整性而 FAIL；D04/D08 仅部分观察；D03/D05/D06/D07/D09/D10 未运行。 |
| 模型校准 | NOT VERIFIED | 合成研究工具有证据。独立真实 outcome 和生理校准仍未验证，RAI VO₂ 保持 experimental。 |
| 回滚 | PARTIAL | 合成 PG 测到发布、切换、回滚与修订审计；previous 全缺口有界修复与完整 in-flight 消费者矩阵未覆盖。 |
| 真实激活 | NOT RUN | 未获授权，执行入口受限于合成 dataset；真实默认仍为 legacy。 |

## E0–E11 实施与剩余范围

| 阶段 | 本轮实际行为 | 剩余范围 |
| --- | --- | --- |
| E0 基线 | 追加迁移 00039–00044，当前 schema 为 44。合成栈为 `rai-v2-b4618722716ba569`，manifest SHA-256 为 `b4618722716ba5695161b967436d6c1ff69b14f6b40aae7bf73ad1c6d109e954`。 | 实际 Feature artifact 仍为 `rai.workout_features/1`，计划中的独立 Feature v2 合同尚未实现。 |
| E1 同步 | 普通 iOS 同步先发送事实并持久化 ACK，完整性证明随后独立续跑；Simulator 覆盖首轮/重复及 lost ACK 原回执重试。 | 普通入口第二增量和真机后台恢复/取消未完成。 |
| E2 输入与来源 | 受限 API 提交类型化输入；日栈使用显式 HR reference、重叠解析及来源筛选；缺显式 RHR 时已接入训练日前 28 日同 epoch 日常 RHR median（≥14 日、前 7 日≥3 日）。 | 同源日常 RHR 的独立来源证据仍受现有 recovery fact 合成映射限制；全部表单和来源变化矩阵未齐。 |
| E3 RAI VO₂ | 独立证据、日截止、稳健估计、敏感性和真实 solver 状态投影；Worker 发布独立 RAI 通道，外部 VO₂ 路径保留。2026-11-08 新增真实隔离 PG18 数值发布 oracle：7 个合格合成 Workout 经日栈发布 `established` 值 35.22244897959183，敏感性 8 角点、LOO 7 折，promotion 后产品/历史/HTTP 均显示 `rai_owned_submax`，外部 VO₂ 槽位独立。 | 真实校准仍未执行；合成 oracle 不等于 criterion 效度。 |
| E4 恢复与准备度 | 日栈读取原始日常事实，计算 Recovery v2，并用同代际 Recovery、Load v2 和 verified proof 计算 Readiness v2；缺失数据保持 null。 | 真实设备 SDNN/Sleep 来源未出现，独立重建等价与全部依赖闭包未齐。 |
| E5 跑力与诊断 | 日栈发布 demonstrated 跑力；maximal 作为 `running_ability_maximal_v2` 研究 artifact 发布；durability/aerobic_efficiency/critical_speed/load_settling 四诊断以 `model_research_v1/<key>` 独立发布。 | 研究诊断尚未在产品详情中展示；真实校准和更多真实样本未验证。 |
| E6 回算 | `rai admin backfill` 执行真实 Worker 日任务，以最多 31 日成块校验 generation、checkpoint、恢复和取消；180 日/180 Workout 已实测（22.52 s）。 | 每个阶段 kill/replay、事实竞争和独立重建 oracle 仍未全部覆盖。 |
| E7 API | 六槽、历史、Workout 详情和训练读同一 active 栈的 clean publication；历史校验 hash、版本、dataset 和 generation，cursor 绑定请求上下文。 | 部分 Worker SQL 计划和跨上下文细节仍未补齐。 |
| E8 iOS 与训练 | 原生 Home、分析历史、Workout 详情接 schema2；真实 App→HTTP→PG→Worker 的 schema2 E2E 已通过（active home、workout detail、history、rollback）。 | 完整原生可访问性、资源预算和训练 in-flight 故障矩阵未齐。 |
| E9 切换 | 合成 gate 绑定 manifest、配置和来源，只接受真实 Worker 生成的完整 clean 候选；回滚检查旧栈，修订号和审计随事务提交。 | previous 全部缺口的有界修复与回滚竞态仍待覆盖。 |
| E10 研究 | 合成研究工具支持化名、独立 outcome、撤回、小组抑制与研究 addendum gate。 | 真实研究授权、outcome、校准和研究诊断发布链的真实数据部分未完成。 |
| E11 证据 | 逐项验收矩阵记录本轮日志及既有历史，失败和 opt-in skip 分开计数；acceptance 当前 99 PASS / 41 PARTIAL / 11 NOT RUN / 1 FAIL。 | 未达到的签核保持 PARTIAL、NOT RUN 或 FAIL。 |

## 实测

| 项 | 结果 | 证据 |
| --- | --- | --- |
| Go 完成门禁（2026-11-07 历史基线） | `scripts/local/test.sh full` 通过：873 s，exit 0，648 个顶层 PASS，0 FAIL，22 个 opt-in skip；隔离库无残留。该日志早于当前 HEAD，不是当前门禁证据 | [full.log](../.local/logs/model-completion-fixes/full-after-research.log) |
| Swift 包 | 238 个测试、34 个 suite 通过 | [swift-after-research.log](../.local/logs/model-completion-fixes/swift-after-research.log) |
| schema2 E2E | Simulator → 真 HTTP → PG → Worker → promote/rollback 全链 PASS（90 s）；active_home/workout_detail/history/rollback 均为 true | [e2e-race.log](../.local/logs/model-completion-fixes/e2e-race.log) |
| F04 | 240 分钟、250,000 点，30 次 P95 1.894872 s、最大 1.905399 s，Feature 42,127 B | [perf-model-norace-v.log](../.local/logs/model-completion-fixes/perf-model-norace-v.log) |
| F07 | 180 日、180 个合成 Workout、6 个 31 日块、180 个历史任务，22.52 s | [perf-model-norace-v.log](../.local/logs/model-completion-fixes/perf-model-norace-v.log) |
| F09 | 同机优化 S60，各 30 次、零请求失败；基线 ACK P95 3,335.2 ms，本轮 3,400.1 ms，增幅 1.95%；旧 1,000 ms 目标仍超限 | [基线](../.local/logs/model-completion-fixes/syncperf-s60-baseline.log) · [本轮](../.local/logs/model-completion-fixes/syncperf-s60-current.log) |
| 真机 D01 | iPhone 12 Pro Max、iOS 27.2、Release 0.6.0(1) 签名安装；只读扫描 100 个 Workout（51 eligible/49 excluded/0 unclassified），原 session manifest 未改变 | [install-result.json](../.local/device-acceptance/install-result.json) · [workout-list-report.json](../.local/logs/model-completion-fixes/device/workout-list-report.json) |
| 真机 D02 | BLOCKED：17 个 Workout 后 `completeness_stream_pending`；一条 Workout 的多个 quantity 返回 `metadata_requires_review`，未生成 receipt/committed anchors | [completeness-report.json](../.local/logs/model-completion-fixes/device/completeness-report.json) · [client-task-state.json](../.local/logs/model-completion-fixes/device/client-task-state.json) |

## G01–G14 追踪

| 项 | 当前证据 |
| --- | --- |
| G01 自有 VO₂ | 日栈发布独立实验槽位；纯域最大容量已测。2026-11-08 真实隔离 PG18 数值发布 oracle 通过（合成值 35.22244897959183，8 角点宽度 <30%，LOO 7 折），管理端 managed candidate 预览可见 `raiVO2Max` 信封。 |
| G02 正式 API | active 与 clean 隔离、当前值、历史和回滚有 PG 与受限角色证据。 |
| G03 iOS | schema2 Home、分析历史和 Workout 详情已接线；schema2 E2E 通过；完整无障碍矩阵未齐。 |
| G04 切换与回滚 | 合成 PG 的 gate、修订审计和切回通过，完整竞态见 X01–X12。 |
| G05 回算 | 数据库 Worker 分块、resume/cancel 和脱敏 diff 已接线，180 日已实测。 |
| G06 Workout 详情 | 真 HTTP 上传后，Worker 发布 Feature，删除后详情撤引用。 |
| G07 训练消费者 | active assignment、v2 Load 与 Readiness 接入，低值仅提示，发布检查修订。 |
| G08 历史趋势 | 按请求绑定 cursor 并校验 artifact，识别跨上下文趋势；as-known 缺输入时返回冲突。 |
| G09 HR 参考 | 显式参考已接入；日常 RHR median 回退已接日栈，来源证据仍受现有 recovery fact 映射限制。 |
| G10 夜间 SDNN | Recovery v2 日栈已接线，真实设备来源待验收。 |
| G11 睡眠规律与偏离 | Recovery v2 已接睡眠目标，独立故障与真实数据仍待验证。 |
| G12 长跑诊断 | 四诊断与 maximal 已接 Worker 研究 artifact 发布；产品详情展示仍待接。 |
| G13 重叠 Workout | 日栈按 resolution 抑制派生重复，原始事实保留。 |
| G14 来源与误标 | 纯域规则和部分日栈筛选已接，来源切换完整矩阵待验收。 |

真实产品未激活；本轮没有读取生产数据库、没有提交凭证、没有迁移真实数据，也没有把 NOT RUN 或 FAIL 写成通过。
