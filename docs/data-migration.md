# 旧版本配置与数据迁移

从支持 `config_schema_version` 的版本开始，应用会在启动时执行可重试的
本地迁移。迁移只补齐缺失结构或记录，不删除用户数据，也不要求管理员先手动
转换旧文件。

## 配置文件

旧 `config.json` 缺少 `config_schema_version` 时，会升级到当前 schema 并补齐
`app_update` 的保守默认值：检查开启、自动下载关闭、自动应用关闭、检查周期
360 分钟。

在回写前，原始文件会复制到：

```text
data/migrations/backups/config-v<old-schema>-<timestamp>.json
```

备份目录权限为仅当前服务用户可读写。迁移只补丁式更新
`config_schema_version` 与 `app_update`，并保留未知的顶层和嵌套字段，避免
旧版本或未来版本的配置被重写丢失。账号 SQLite 已成功导入后，旧 JSON 中的
`accounts` 会移除，避免把密码和 token 继续重复保存在配置文件中。

通过 `DEEPSEEK_WEB_TO_API_CONFIG_JSON` 注入且未启用
`DEEPSEEK_WEB_TO_API_ENV_WRITEBACK` 的配置只在内存中应用默认值，不会写入
环境变量来源。

## data 目录

启动路径会自动处理下列持久化数据：

| 数据 | 迁移行为 | 数据保护 |
| --- | --- | --- |
| `accounts.sqlite` | 创建缺失列和索引；旧 JSON 账号增量合并。 | 已存在账号以 SQLite 运行态为准，不覆盖 token、禁用状态或代理绑定。 |
| `chat_history.sqlite` | 创建缺失表/索引/列，压缩旧详情，标记上次中断请求。 | 旧 `chat_history.json` 按内容哈希增量导入；同 ID 的 SQLite 记录不被旧 JSON 覆盖。 |
| `token_usage.sqlite` | 创建统计表并一次性导入旧历史聚合。 | 使用迁移标记避免累计值重复相加。 |
| `safety_words.sqlite` / `safety_ips.sqlite` | 创建策略表，并首次导入旧 JSON 安全列表。 | 迁移标记保证幂等，不清空现有规则。 |
| `response_cache/` | 缓存格式不兼容或过期时自动失效。 | 缓存是可再生数据，不影响账号、配置或历史。 |
| `xray/` | 保留现有核心、geo 数据和版本标记；缺失时按现有下载策略补齐。 | 不重置已下载核心或代理配置。 |
| `self-update/` | 保留已验证版本、回滚标记和失败隔离标记。 | 不触碰 `config.json`、SQLite、缓存或 Xray 数据。 |

SQLite schema 变更在事务中执行，所有迁移均可安全重复运行。若某个可选数据
库不可用，服务会记录错误并继续启动其他组件；修复路径或权限后，下次启动会
再次尝试该库的迁移。

## Docker 旧布局

旧 Compose 曾把宿主机根目录的 `./config.json` 映射到容器 `/app/config.json`；
当前 Compose 只持久化 `./data:/app/data`。升级时，`legacy-config-migration`
一次性服务会先以与应用相同的非 root 用户运行：它独占读取项目根目录挂载的
`/legacy/config.json`，并只写入 `/app/data/config.json`。

首次使用该桥接版本升级镜像后，先执行 `docker compose pull`，再执行
`docker compose up -d`，以确保初始化服务使用包含迁移工具的新镜像。

- 目标已存在、旧文件不存在时，服务成功退出且不修改任何文件。
- 仅在目标不存在时，服务先在 `data` 目录创建私有临时文件，再通过同文件系统的
  原子链接发布完整配置；它绝不覆盖已有目标。
- 旧文件不可读、目标目录不可写或原子发布失败时，迁移服务失败，主应用因 Compose
  依赖不会以默认配置启动。可通过 `docker compose logs legacy-config-migration`
  排查权限后重试。
- 项目根目录只挂载给这个短生命周期服务，主应用容器始终只挂载 `./data`，不会取得
  根目录或旧配置之外的宿主机文件访问权。

配置桥接完成后，应用启动时的 schema、账号、历史、统计和安全策略迁移继续处理
`/app/data` 中的持久化数据，无需手工搬运文件。
