# Historical Rust v0.2.0 / 历史版本

当前运行时与开发入口已迁移到 **Go**，请使用 [Go CLI 与 Agent 指南](go.md)、[安装指南](install.md)和[迁移记录](go-migration.md)。当前构建、CI、Docker 与安装器不再依赖 Rust。

历史 `v0.2.0` 的源码、标签和发布附件保持不变：

- [历史源码及完整指南](https://github.com/huaiwen/filewise/blob/v0.2.0/docs/rust.md)
- [历史 Release](https://github.com/huaiwen/filewise/releases/tag/v0.2.0)

旧数据库与 Go 状态格式分开；不隐式转换，也不把历史二进制作为 Go 安装回退。

The active runtime is Go. The immutable v0.2.0 source and release remain available above as historical references. Use separate Go state; existing databases are not automatically imported or rewritten.
