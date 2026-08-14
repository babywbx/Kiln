# 参与贡献

[English](./CONTRIBUTING.en.md)

感谢你愿意花时间。这里先说清楚流程，免得你写完才发现方向不对。

## 先开 issue

**任何改动都从一个 issue 开始，要不要提 PR 在 issue 里再定。**

这不是把门关上，而是想让你少做无用功。项目由一个人维护，路线图和取舍还没有全部写进文档；直接送上来的 PR 有可能和已经在做的事情撞车，也可能因为一个你无从得知的约束而无法合并。先在 issue 里花十分钟对齐，比写完两百行再被拒绝划算得多。

流程是这样：

1. 先搜一遍现有 issue，可能已经有人提过
2. 按模板开 issue，缺陷用缺陷模板，想法用功能模板
3. 我们在 issue 里确认这件事要不要做、怎么做
4. 谈定之后，你或者我来实现

拼写订正、失效链接这类一眼就能判断的改动，可以直接提 PR，不必先开 issue。

**安全问题不要走这条路。** 请看[安全策略](./SECURITY.md)，用私密安全公告报告。

## 报告缺陷

一份能直接开工的报告需要：版本号、变体（Lite、Core 或 Full）、部署方式、`[packager].engine` 的取值，以及相关日志。缺陷模板会逐项问你。

贴配置和日志之前，请移除口令、API Token、播放密钥、解密密钥与真实源站地址。

## 开发环境

| 依赖 | 版本 | 用途 |
| --- | --- | --- |
| Go | 1.26.6 | 服务端 |
| pnpm | 见 `package.json` | 文档站与前端检查 |
| Docker | 任意近期版本 | 镜像与容器测试，可选 |

常用命令：

```bash
make ci               # 提交前必须通过
make build            # 本地调试构建
make docker-images    # 构建 Lite、Core 与 Full 三个镜像
make test-complete    # 完整本地验证，比 CI 更慢也更全
```

文档站单独检查：

```bash
cd apps/docs
pnpm run typecheck && pnpm run lint:docs && pnpm run test:semantics
```

## 提交前的门槛

**PR 必须在本地通过 `make ci`。** 它涵盖格式化、`go vet`、跨平台交叉检查、golangci-lint、竞态检测的单元测试、Lite 契约、管理台前端测试、Docker target 检查、安装脚本契约与漏洞扫描。CI 上跑的是同一套。

改动涉及文档站时，另外跑一遍上面那三条 `apps/docs` 检查。

请不要在 PR 里附带无关的格式化改动或依赖升级，它们会淹没真正的改动，请单独开 issue。

## 提交信息

单行、英文、主题小写，格式为：

```text
<emoji> <type>(<scope>): <subject>
```

类型与 emoji 的对应：

| 类型 | Emoji | 类型 | Emoji |
| --- | --- | --- | --- |
| `feat` | ✨ | `test` | ✅ |
| `fix` | 🐛 | `build` | 📦 |
| `docs` | 📝 | `ci` | 💚 |
| `style` | 🎨 | `chore` | 🔧 |
| `refactor` | ♻️ | `revert` | ⏪ |
| `perf` | ⚡️ | | |

例如：

```text
🐛 fix(packager): honour EssentialProperty when choosing representations
```

主题写清楚做了什么。需要解释原因时写在正文里，说明改之前是什么状况、为什么这样改，不要复述 diff。

## Pull Request

- 从 `main` 切分支，一个 PR 只做一件事
- 在描述里链接对应的 issue
- 行为有变化时补上测试，媒体相关的改动请说明用什么验证的
- 面向用户的改动要同步更新 `apps/docs` 下的中英两份文档
- 保持提交历史整洁，评审过程中的修补请合并进相关提交

## 许可证

本项目使用 AGPL-3.0-only。提交 PR 即表示你同意你的贡献以同一许可证发布，并且你有权这样做。项目不使用贡献者许可协议。

如果你的代码来自其它项目或包含由工具生成的内容，请在 PR 里说明来源与许可证。无法确认来源的代码不会被合并。

## 行为准则

参与本项目即表示你接受[行为准则](./CODE_OF_CONDUCT.md)。
