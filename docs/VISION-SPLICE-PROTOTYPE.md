# Linux Vision 可调速 splice 原型（默认关闭）

> -19 发布状态：包含默认 Vision 基础 splice、动态窗口容量和无配额等待路径修补。Vision 可调速原型与 AnyTLS QUICKACK 仍默认关闭；发布不等于已部署。下文早期记录按其日期和版本理解，当前兼容性、验收及性能边界以 [发布说明](RELEASE-0.4.1-19.md) 为准。

本地开发基线：96cc201，承接面板动态限速候选。不是对 -17 的自动启用变更。本轮不修改面板、订阅、数据库、服务器配置或线上程序。

## 配置与范围

显式配置某个 Xray 节点的 `EnableVisionSplice: true` 才启用可调速原型；建议同时明确 `Core: "xray"`。参数与 `EnableProxyProtocol` 同级，省略/false 不启用原型或 Vision 动态策略。默认无限速 Vision 的基础 splice 修补见 [基础路径说明](VISION-SPLICE-BASELINE.md)。配置按节点隔离，开关变更需要重建入站，不是现有连接上的即时开关；开启后的限速策略更新无需断开长连接。

- Linux + VLESS Vision：在上游判定允许直传后，使用可取消的 socket → kernel pipe → socket splice。TLS 握手、Vision 填充与协议识别沿用原实现。
- 确认的零拷贝范围是服务端 freedom **下行**。上行保留原有 Vision 解码/缓冲路径并限速，不宣称新增了上行零拷贝。
- TLS 1.2、未识别为可直传的 TLS 或原有不适用 splice 的传输，继续经原缓冲路径处理和限速，不强行穿透加密。
- 非 Linux 显式启用会拒绝该节点启动；默认关闭的 Windows 等平台保持兼容。
- 原型关闭时不加入配额循环或发送原型能力头。符合原零拷贝条件且没有固定限速的 Linux Vision 可使用基础 splice，它独立保留写前计费与撤销能力；固定限速及不符合条件的连接继续走缓冲路径。仍需性能实测，不能承诺所有硬件绝对零开销。

## 调度、授权和计费

不限速时每次尽量大块搬运；有限速时以不超过 16 KiB 的额度分块，等待后仍用 splice，不改为用户态复制。配额复用已有每节点、每用户代际的动态控制器和固定桶，同一节点多个连接/上传下载共享。动态上限不能突破原固定上限。无策略时保持快速检查路径；策略更新唤醒正在等待的长连接。

管道中的数据不会重复计费：每次实际写入目标 socket 的成功字节，更新原方向的用户计数、动态观察量及原传输计数各一次。缓冲段继续原计数点，上行沿用入口计数；不把所有协议改成“最终用户收到了才计费”。可靠上报、批次、倍率及 API 格式均未修改。

splice 被登记为原 LinkManager 的在途操作，撤销/排空会关闭连接和取消等待，在最后一次计数完成前不回收代际。内核管道退出即关闭，不使用新的后台长期线程。每个活动 splice 占两个 fd；实际管道大小受内核限制，最大申请 1 MiB，不要求提权或更改内核参数。

路由所需用户标签仍使用原 metadata 副本；原型及默认基础 splice 连接共享原子化的 Vision 就绪状态。下行必须等自己的 writer 完成切换后才能进入 splice，不能只凭上传 reader 已切换就提前直传。这避免将 TLS/Vision 尚未完成的字节绕过协议处理。

## 面板配套：仅接口约定，本轮未部署

继续 `GET /api/v1/server/UniProxy/user`、原鉴权及 JSON/MessagePack `dynamic_speed_limit` v1 字段，不新增数据库迁移或计费接口。

- 未启用原型：`X-V2bX-Dynamic-Speed-Limit: 1` 与 `X-V2bX-Dynamic-Speed-Limit-Exclude: xray-vless-vision`。
- Linux Xray 显式启用原型：保留 v1 能力头，额外发送 `X-V2bX-Vision-Splice-Prototype: 1`，不再发送 Vision 排除头。
- 面板须仍显式允许实验节点，按能力区分响应和 ETag；不能仅因排除头缺失就向旧节点下发 Vision 策略。未完成此配套时可能仍没有策略，但正常连接和固定限速继续工作。
- 自动选核在 AddNode 才确定核心时，第一次用户拉取保守排除 Vision；确定为已启用原型的 Xray 后失效用户缓存，下轮重新取策略。显式 Core=xray 可在首次请求声明。
- 用户 id/uuid、设备额度、订阅 flow、真实 IP、speed_limit 与动态字段语义不变。关闭动态规则不等于关闭原型，恢复无限速仍保留原固定上限。

## 本地复跑

Go 1.25、`GOEXPERIMENT=jsonv2`。普通回归：

```sh
GOEXPERIMENT=jsonv2 go test -race ./...
```

真实 Vision 集成需要 Linux 和显式测试 tag：

```sh
GOEXPERIMENT=jsonv2 go test -tags vision_live -race \
  -gcflags=github.com/xtls/xray-core/proxy/vless/...=-d=checkptr=0 \
  ./core/xray -run TestVision -count=3 -timeout=180s
GOEXPERIMENT=jsonv2 go test -race ./common/visionsplice ./common/rate ./core/xray/app/dispatcher
```

上述专用命令仅对既有 VLESS unsafe 依赖关闭 checkptr，**race 检测仍开**。夹具固定证书并关闭测试证书热加载，以隔离依赖原有的证书 reload 竞争；没有改变生产证书配置，也不表示依赖的上述问题已修复。测试使用临时证书、随机测试身份、本机目标、合成 PROXY v2 地址，无实际订阅或业务凭据。零拷贝专项先交换两个完整的 64 字节 TLS 应用记录，使初始记录边界明确；直接以大块数据作为首包时，上游完整记录识别可能保留缓冲路径。该测试准备没有改动生产协议检测逻辑。

`TestVisionRealProfile` 和 `TestVisionSpliceProfile` 由 `VISION_PROFILE_DURATION` / `VISION_PROFILE_MODE`（legacy/managed/observe）/ `VISION_PROFILE_PARALLEL` 显式启用。性能用非 race 测试二进制，不能把 race 耗时当作运行开销。CPU 包括同进程的本地客户端、代理、目标服务；RSS 是进程峰值，不能当作纯节点内存增量。

## 回退与未验证边界

关闭原型并重建节点可恢复默认基础路径；原型期内形成的可靠批次照常发送，不删除/改写，不需要数据库回滚。回退前按既有 drain/checkpoint 顺序停止入站并保存计数。默认 Vision 排除策略恢复，不会继续承诺 Vision 动态限速。

本轮只做本地 Linux VM 和 macOS 隔离测试：不等于蓝绿机、测试 VPS、公网、真实客户端订阅或生产验证。Linux amd64/armv7 与 Windows 只做交叉编译；运行实测为 Linux arm64。尚未验收 Reality、各类隧道/级联代理、真实 Quantumult X、超长运行/大规模 fd 压力或所有 TLS 握手分片组合。满足条件时 splice 实际成功；不保证任何 TLS 流都被上游识别为可直传。
