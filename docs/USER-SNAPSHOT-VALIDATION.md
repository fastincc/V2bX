# 用户列表完整性校验

本修补针对 -19 用户列表响应结构异常时可能误撤销用户的问题，仅调整节点侧解析与缓存确认，不新增面板接口、配置或数据库迁移。

## 响应约定

- HTTP 200 的 JSON 和 MessagePack 都必须是一个完整顶层对象，明确包含一次 `users` 数组，数组元素须为对象。允许未知扩展字段；用户原有字段解码及无效动态策略的隔离语义不变，不增加 UUID 格式限制。
- 明确的 `{"users":[]}` 仍是有效撤销全部用户的快照。HTTP 304 仍代表无更新，不能与空数组混同。
- 缺失 `users`、`users:null`、嵌套冒充、重复 `users`、截断、尾部额外文档/垃圾和无法解码的用户对象均拒绝整批应用。MessagePack 的顶层键使用规范字符串编码，不能依赖旧解码器对二进制键的宽松处理。
- 解析失败不调用用户增删、设备更新或计费归属变更，不提交本次 ETag；清除条件请求缓存以便下次获取完整快照。最后有效授权保留，初次启动尚无有效快照时仍返回错误。
- 错误信息不包含响应正文、解码器原始错误或凭据。MessagePack 不按不可信数组长度一次性申请全部内存。
- 固定版本 MessagePack v5.4.1 的本地副本检查 32 位长度转换，防止 ARMv7 将超长嵌套字段误判为空值；扩展类型单独读取，避免长度加一溢出。保留原版本、许可证和上游校验清单，不升级依赖，不对响应做第二遍扫描。

本修补校验传输结构，并不判断面板业务决策是否正确。结构合法的清空、用户删除或字段变更仍按现有接口执行；不是用户 ID/UUID 业务规则重写。面板持续返回异常结构时，新增与撤销都会延迟，需修复面板响应；不得把错误响应当成撤销指令。

## 兼容性和性能

身份认证、订阅、真实 IP、设备限制、静态/动态限速、流量方向、可靠批次 ID 和持久队列格式不变。`EnableVisionSplice` 与 `AnyTLSAdaptiveQuickACK` 的默认值仍为 false。

解析只发生在控制面的用户同步，采用逐项解析，未改数据转发路径。完整校验增加部分控制面工作；大列表解析耗时和分配量须单独报告，不能声称修补零开销。没有 ETag 的面板仍可工作。

## 回归入口

- `TestUserListRejectsMalformedSnapshot`、`TestUserListValidEnvelopeAndRecovery`、`TestUserListRequiresSuccessStatus`：两种格式、扩展字段、坏响应及缓存恢复。
- `TestUserResponseInvalidSnapshotPreservesAppliedState`：经 HTTP 拉取和节点监控，在在线状态正常/故障时保护旧授权、设备和流量状态；恢复合法空数组仍准确撤销。使用模拟 core 与设备包装的内存连接，不宣称真实代理链路验收。
- `TestDynamicPolicyWireAndETag`：无效动态扩展不误撤销有效用户。
- `TestUserListMessagePackLengthBoundaries`、`TestUserListMessagePackValidWideLengthHeaders`：顶层、用户和动态策略的超长字段拒绝，以及合法宽长度头兼容；需要同时执行 32 位运行测试，不能仅以交叉编译或 64 位通过替代。
- `FuzzUserListEnvelope`：异常输入不暴露部分列表或原始解码错误。
- `BenchmarkUserListHTTP`：100/10000 用户、JSON/MessagePack，包含本地 HTTP 成本，可与 -19 同测试负载对照。

本地独立验收记录位于工作区 `audits/2026-11-08-three-fixes/`；发布与部署需另行授权。
