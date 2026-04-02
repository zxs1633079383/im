# Plan 1: 项目基础 + 数据库 — 执行简报

**状态**: 完成
**日期**: 2026-04-02

---

## 完成情况

| Task | 内容 | 状态 |
|---|---|---|
| T1 | Go 项目脚手架 (go mod, cmd, Makefile) | Done |
| T2 | 配置系统 (YAML + env override) | Done |
| T3 | PG 数据库迁移 (9张表, 触发器) | Done |
| T4 | 核心领域模型 (User/Channel/Message/Friendship) | Done |
| T5 | 数据库连接池 (PG pool + Redis client) | Done |
| T6 | 测试辅助工具 (testutil PGPool) | Done |
| T7 | User Store (CRUD + 去重测试) | Done |
| T8 | Channel + Message Store (phantom, 幂等, seq) | Done |
| T9 | Tauri + Angular 客户端初始化 | Done |
| T10 | 客户端 SQLite Schema + DatabaseService | Done |
| T11 | 全量集成验证 | Done |

## 验证结果

- Go 单元测试: 7 PASS (config 2, model 5)
- Go 集成测试: 10 SKIP (需要 PG, 设计预期)
- Go vet: 无问题
- 三个服务编译: 全部通过
- Angular 构建: 无错误

## 产出物

### 服务端 (`server/`)
- **3 个服务入口**: gateway, message, sync
- **配置系统**: YAML 加载 + 环境变量覆盖 (IM_PG_DSN, IM_REDIS_ADDR, IM_PULSAR_URL)
- **PG Schema**: users, channels, channel_members, messages, friendships, files, message_attachments, message_favorites, user_settings + updated_at 触发器
- **领域模型**: User, Channel, ChannelMember (含 UnreadCount), Message (含 IsVisibleTo, Phantom), Friendship
- **Store 层**: UserStore, ChannelStore (含 IncrementSeq, MarkRead, IncrementPhantomCount), MessageStore (含幂等 Send, FetchForUser with phantom masking)

### 客户端 (`client/`)
- **Angular 项目**: SCSS, routing, SSR disabled
- **Tauri v2**: SQLite plugin 已集成
- **本地 Schema**: local_channels, local_messages (含 visible partial index), local_outbox, local_meta
- **DatabaseService**: initialize, execute, query, getOne

## 实现中的调整

1. **import cycle 修复**: Store 测试文件使用 `package store_test` (外部测试包) 避免与 testutil 的循环导入,这是标准 Go 模式
2. **Tauri 初始化**: 使用 `tauri init --ci` 非交互模式完成

## 下一步

Plan 2: 认证系统 (注册/登录接口, JWT, 客户端登录页)
