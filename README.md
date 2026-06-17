# Node Latency Watch

三网 Agent 模式的机场全节点延迟曲线测试原型。主控负责拉取订阅、解析节点、下发测试任务、汇总样本和展示曲线；Agent 部署在电信、联通、移动等线路机器上，主动拉任务并上报结果。

这个项目刻意去掉了 Cloudflare Token、DNS A 记录切换和入口路由选优逻辑，只保留全节点观测需要的部分。

## 当前能力

- 主控读取 `providers` 订阅配置
- 支持 Clash YAML 订阅和常见分享 URI 行解析
- Agent 主动拉取 `/api/agent/jobs`
- Agent 测量节点的 DNS、TCP、可选 TLS 握手延迟
- 主控接收 `/api/agent/reports`
- SQLite 保存节点样本
- 内置 Web Dashboard 显示三网实时矩阵和最近样本

## 快速运行

复制配置：

```powershell
Copy-Item config.example.yaml config.yaml
Copy-Item agent.example.yaml agent.yaml
```

启动主控：

```powershell
go run ./cmd/controller config.yaml
```

启动 Agent：

```powershell
go run ./cmd/agent agent.yaml
```

浏览器打开：

```text
http://127.0.0.1:19200
```

## 构建

```powershell
go build -o node-latency-controller.exe ./cmd/controller
go build -o node-latency-agent.exe ./cmd/agent
```

Linux Agent 二进制供主控一键安装下载：

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; go build -o bin/node-latency-agent-linux-amd64 ./cmd/agent
$env:GOOS='linux'; $env:GOARCH='arm64'; go build -o bin/node-latency-agent-linux-arm64 ./cmd/agent
Remove-Item Env:GOOS, Env:GOARCH
```

主控启动后，后台 `管理设置 / Agent 探针` 会生成类似 Nezha 的安装命令。脚本优先从当前主控下载 `/api/agent/download/<target>`，失败时回落到 GitHub Release：

```bash
curl -fsSL http://主控:19200/install.sh | sudo bash -s -- --controller http://主控:19200 --id ct-ningbo-01 --token '<通信Token>'
```

## 配置要点

- `agent.token`：主控和 Agent 的共享通信 Token
- `providers[].subscription_url`：机场订阅地址
- `providers[].subscription_file`：本地订阅文件，可用于离线调试
- `probe.tls_mode`：
  - `auto`：trojan/vless/vmess/hysteria2 或 443 端口测 TLS
  - `always`：所有节点都测 TLS
  - `never`：只测 DNS/TCP

注意：这里“不接触域名解析”指不再管理 Cloudflare DNS 记录。节点服务器如果本身是域名，Agent 仍需要做一次普通解析才能连接。
