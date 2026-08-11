# Xray 代理协议

应用可以把 VLESS、VMess 和 Hysteria2/HY2 节点转换为临时 Xray 配置，并通过仅监听 `127.0.0.1` 的本地 SOCKS 入口供账号请求使用。SOCKS5/SOCKS5H 仍由应用直接连接，不依赖 Xray。

## Windows 安装

1. 从 XTLS/Xray-core 官方 release 下载 Windows 压缩包。
2. 将 `xray.exe`、`geoip.dat` 和 `geosite.dat` 放到应用可执行文件同目录；也可以在代理管理页填写完整的 `xray.exe` 路径。
3. 打开 `/admin` 的代理管理页，确认核心状态显示可用及版本号，再添加节点链接并执行连通性测试。

核心查找顺序如下：

1. `proxy_core.xray_binary_path`；已配置但文件不存在时直接报告该路径错误。
2. 环境变量 `XRAY_BINARY_PATH`；已配置但文件不存在时直接报告该路径错误。
3. 应用可执行文件同目录的 `xray.exe`，或其 `bin/xray.exe`。
4. 系统 `PATH` 中的 `xray.exe`。

Linux 使用同样的查找顺序，文件名为 `xray`。当前实现已使用官方 Xray `v26.3.27` 的 Windows amd64 二进制执行配置校验和进程生命周期集成测试。

## 配置

```json
{
  "proxy_core": {
    "xray_binary_path": "C:\\tools\\xray\\xray.exe",
    "runtime_dir": "data\\xray-runtime",
    "startup_timeout_seconds": 10
  },
  "proxies": [
    {
      "name": "VLESS node",
      "type": "vless",
      "uri": "vless://UUID@example.com:443?encryption=none&security=tls&sni=example.com"
    }
  ]
}
```

`xray_binary_path` 和 `runtime_dir` 留空时使用自动发现与系统临时目录。启动超时允许 `1-60` 秒，`0` 表示使用内部默认值 10 秒。

管理 API：

- `GET /admin/proxies/core`：返回核心是否可用、版本、实际路径、运行实例数和错误原因。
- `PUT /admin/proxies/core`：更新核心路径、运行目录和启动超时，并停止旧实例。
- `POST /admin/proxies`：`type` 可为 `vless`、`vmess`、`hysteria2` 或 `hy2`，节点链接放在 `uri`。
- `POST /admin/proxies/test`：使用已保存的 `proxy_id` 测试完整代理链路。

节点 `uri` 可能包含 UUID、认证字符串等秘密。新增、编辑、列表和安全配置读取接口只返回 `has_uri`，不回显原始链接；编辑时 URI 留空会保留已有值。完整配置导出仍包含秘密，只应保存到可信位置。

## 支持范围

| 类型 | 输入格式 | 当前支持 |
| --- | --- | --- |
| VLESS | `vless://...` | TCP/RAW、WebSocket、gRPC、XHTTP/SplitHTTP、HTTPUpgrade；TLS、REALITY |
| VMess | 标准 base64 JSON `vmess://...` | `alterId=0`；TCP、WebSocket、gRPC、XHTTP/SplitHTTP、HTTPUpgrade；TLS |
| Hysteria2 | `hysteria2://...` 或 `hy2://...` | Xray Hysteria version 2、TLS、auth |

为避免静默降级，以下配置会在保存阶段直接拒绝：

- `insecure=1` 或 `allowInsecure=1`：Xray v26.3.27 已移除 `allowInsecure`，请使用有效证书。
- Hysteria2 `obfs` / `obfs-password`：当前 Xray Hysteria 集成不支持该分享链接能力。
- Hysteria2 `pinSHA256`：当前转换层尚未实现对应字段。
- VMess `alterId` 非 0。

## 运行与日志

核心代理第一次真正建立连接时按需启动。每个代理 ID 对应一个 Xray 进程，后续连接复用其本地 SOCKS 入口；节点或核心设置改变、节点删除、配置导入、应用退出时会停止对应实例。

默认运行目录为 `%TEMP%\DeepSeek_Web_To_API-xray\process-<pid>`（Windows）或系统临时目录下的同名路径。Xray 配置文件权限设为仅当前用户可读，并在进程退出后删除；运行日志保留为 `*.log` 供排查。应用主日志会记录代理 ID、协议、本地端口、Xray PID、启动/停止和失败原因，但不会记录节点 URI。

建议通过管理页的“测试”按钮验证节点。若失败，先查看 `/admin/proxies/core` 的 `status.error`，再查看返回错误中指向的 Xray runtime log。

## 官方来源

- [XTLS/Xray-core](https://github.com/XTLS/Xray-core)
- [Xray-core releases](https://github.com/XTLS/Xray-core/releases)
