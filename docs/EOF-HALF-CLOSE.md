# EOF 与半关闭能力保持

此前计费、静态限速和动态限速包装器无条件提供 `CloseRead` / `CloseWrite`。当底层 AnyTLS 逻辑流不支持半关闭时，辅助函数返回成功但未关闭流；路由复制任务因而不执行完整关闭，客户端收到尾部数据后仍可能一直等待结束。

现在在创建包装器时分别探测读、写半关闭能力，仅对支持该能力的传输提供对应方法。无半关闭能力的流沿用路由已有的完整关闭分支；真实 TCP 半关闭仍保留反向传输。没有修改 AnyTLS 协议或引入接收缓冲。

## 兼容边界

- 配置、订阅、面板接口、可靠上报格式及上下行计费方向不变，无数据库迁移。
- 不增加 `Upstream` 或其他可绕过限速、计费的展开接口；读写计数与静态/动态配额算法未改。
- EOF 完整关闭会释放活动计费所有者、取消两侧等待并释放活动设备连接；既有设备宽限期仍保留。
- 同一 AnyTLS TLS 会话中结束一条逻辑流，不应关闭其他流。固定依赖的客户端将远端 FIN 映射为 `net.ErrClosed`；验收要求完整尾部数据和及时结束，不承诺错误值一定为字面 `io.EOF`。
- AnyTLS 不支持真正的 TCP 半关闭。本补丁修复结束信号挂起，不新增“单向结束后仍继续反向传输”的 AnyTLS 协议能力。
- `NewConnRateLimiter` 的 Go 返回类型由 `*Conn` 改为 `net.Conn`，以返回不同能力类型。仓库内调用方已编译验证；直接依赖这个 Go API 的外部源码需通过接口断言使用可选半关闭方法。对部署配置与网络接口无影响。
- `EnableVisionSplice` 和 `AnyTLSAdaptiveQuickACK` 仍默认关闭。

## 性能设计

能力选择只在连接构造时执行。各具体类型内嵌原有包装器，不增加逐次读写包装层；未增加数据拷贝、接收缓存、逐包能力检测或异步队列。能力判断存在少量建连成本，不能表述为绝对零开销。

## 回归入口

- `TestDuplexCapabilitiesMatchTransport`：4 种能力与 7 种包装组合。
- `TestDuplexUnsupportedEOFFullyCloses`：不支持半关闭时真实 EOF 与所有者释放。
- `TestDuplexTCPHalfCloseKeepsReverseTraffic`：TCP 半关闭、反向尾部数据和精确计数。
- `TestDuplexEOFCancelsRateWaits`：两侧静态/动态等待取消，已传输字节保留，未发送字节不计费。
- `TestEOFCloseReleasesDeviceWithoutRemovingGrace`：活动设备释放，不绕过宽限期。
- `TestAnyTLSTargetEOFClosesStreamAndAccountsTail`：真实 TLS/AnyTLS 与生产 ConnectionManager，目标关闭后完整接收 65544 字节，5 种限速状态、合成 PROXY 真实 IP、计费及重复连接释放。
- `TestAnyTLSEOFKeepsSiblingStream`：明确使用同一 TLS 会话，另一条流在 EOF 前后持续收发并准确计费。

主要命令（Go 1.25，`GOEXPERIMENT=jsonv2`）：

```sh
go test -race ./common/counter ./common/rate ./limiter -count=1
go test -race -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' ./core/sing ./core/xray ./api/panel ./node -count=1
go test -tags 'sing with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' ./core/sing -run 'TestAnyTLS(TargetEOF|EOFKeepsSibling)' -count=5
go test ./conf -count=1
go test ./common/rate -run '^$' -bench '^BenchmarkDuplexUnlimited$' -benchmem -count=3
```

远端吞吐与 EOF 分开验证：K/F 应用确认用于性能工具完成判定，不能代替 EOF 验收。两机隔离回环、合成 IP/用户及内存计数测试不能等同于公网客户端、面板数据库对账或生产验证。

## 2026-11-11 验收记录

相对修补前 `22c063c`：蓝绿与测试 VPS 各 3 次旧代码复现关闭超时，候选各 3 轮 EOF/兼容测试和各 5 轮包装器测试通过；各完成 80 流关闭并等待客户端清理，goroutine 恢复初始值。每机 6 组冒烟与 28 组正式 CLI 性能样本全部完成。

蓝绿单向上传/下载均值变化 +0.27%/+8.65%，双向上传/下载 −0.42%/−1.45%；VPS 分别 +3.31%/−0.30%、+1.52%/+0.40%。未观察到本补丁造成持续性不限速下降，但样本有波动，不能承诺所有场景不降速或宣称统计等效。微基准无逐次传输分配，建连/关闭中位开销约增加 20 ns；TCP 能力包装调用中位约增加 0.34 ns。

相关完整 race、vet、配置默认值和四平台编译通过。两机正式服务及程序/配置/服务哈希保持原状，临时测试资源已清理。详细证据在维护工作区 `audits/2026-11-11-eof-close/`。本次不是生产、真实客户端或面板数据库验收，也不自动解除此前其他修补的性能疑点。
