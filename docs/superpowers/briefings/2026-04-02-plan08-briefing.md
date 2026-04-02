# Plan 8: 客户端聊天核心 — 执行简报

**状态**: 完成
**日期**: 2026-04-02

---

## 完成情况

| Task | 内容 | 状态 |
|---|---|---|
| T1 | Chat header (频道名, 成员数, 设置入口) | Done |
| T2 | Message rendering (分组, 时间戳, 日期分隔, 系统消息) | Done |
| T3 | Auto mark-read (进入频道清除未读) | Done |
| T4 | DM creation flow (联系人→建DM→聊天) | Done |
| T5 | Channel list sorting + search filter | Done |
| T6 | Reply-to message (右键菜单, 回复条, 引用预览) | Done |
| T7 | Jump to bottom indicator (新消息按钮) | Done |
| T8 | Integration verification | Done |

## 验证结果

- Go 测试: 69 PASS, 22 SKIP
- Angular 构建: 成功

## 产出物

- **Chat header**: 频道名/DM名 + 成员数 + 设置齿轮
- **消息渲染**: 同发送者5分钟内合并, 日期分隔符, 系统消息居中斜体, 头像首字母
- **Auto mark-read**: 进入频道即清零未读, 活跃频道收到新消息自动已读
- **DM 创建**: 联系人列表 "Message" 按钮 → createOrGetDM → 导航到聊天
- **频道列表**: 按最新消息时间排序, 搜索过滤, 在线状态圆点(占位)
- **Reply-to**: 右键菜单(回复/复制), 回复条, 气泡内引用预览
- **Jump to bottom**: 滚动离底部时显示 "↓ New messages" 蓝色按钮

## 下一步

Plan 9: 搜索 (PG 全文搜索, 全局搜索页面, 消息跳转)
