# Contributing

[简体中文](./CONTRIBUTING.md)

Thanks for spending time on this. The process is stated up front so you do not discover the direction was wrong after writing the code.

## Open an issue first

**Every change starts as an issue. Whether a pull request follows is decided there.**

This is not a closed door, it is an attempt to save you wasted work. The project has one maintainer, and the roadmap and its tradeoffs are not all written down yet. A pull request that arrives unannounced may collide with work already in progress, or may be unmergeable because of a constraint you had no way to know about. Ten minutes of alignment in an issue beats two hundred lines that get turned down.

The flow:

1. Search the existing issues, someone may have raised it already
2. Open an issue using a template, the bug form for defects and the feature form for ideas
3. We settle in the issue whether the change should happen and how
4. Once that is agreed, you or I implement it

Typo fixes and dead links are obvious on sight and can go straight to a pull request without an issue.

**Security problems do not follow this path.** See the [security policy](./SECURITY.en.md) and report through a private advisory.

## Reporting a bug

A report that can be acted on needs the version, the variant (Lite, Core or Full), the deployment method, the value of `[packager].engine`, and the relevant logs. The bug form asks for each of these.

Before pasting configuration or logs, remove passwords, API tokens, play keys, decryption keys and real upstream addresses.

## Development environment

| Requirement | Version | Used for |
| --- | --- | --- |
| Go | 1.26.5 | The server |
| pnpm | See `package.json` | Documentation site and frontend checks |
| Docker | Any recent version | Images and container tests, optional |

Common commands:

```bash
make ci               # Must pass before you submit
make build            # Local debug build
make docker-images    # Build the Lite, Core and Full images
make test-complete    # Full local verification, slower and broader than CI
```

Checking the documentation site on its own:

```bash
cd apps/docs
pnpm run typecheck && pnpm run lint:docs && pnpm run test:semantics
```

## The bar before you submit

**A pull request must pass `make ci` locally.** It covers formatting, `go vet`, cross platform checks, golangci-lint, unit tests under the race detector, the Lite contract, admin console frontend tests, Docker target checks, the install script contract and a vulnerability scan. CI runs the same set.

When a change touches the documentation site, also run the three `apps/docs` checks above.

Please do not bundle unrelated reformatting or dependency bumps into a pull request. They bury the actual change, so raise them as their own issue.

## Commit messages

One line, English, lowercase subject, in this shape:

```text
<emoji> <type>(<scope>): <subject>
```

Types and their emoji:

| Type | Emoji | Type | Emoji |
| --- | --- | --- | --- |
| `feat` | ✨ | `test` | ✅ |
| `fix` | 🐛 | `build` | 📦 |
| `docs` | 📝 | `ci` | 💚 |
| `style` | 🎨 | `chore` | 🔧 |
| `refactor` | ♻️ | `revert` | ⏪ |
| `perf` | ⚡️ | | |

For example:

```text
🐛 fix(packager): honour EssentialProperty when choosing representations
```

The subject says what the change does. When the reason needs explaining, put it in the body: what the situation was before, and why this is the fix. Do not restate the diff.

## Pull requests

- Branch from `main` and keep one pull request to one concern
- Link the issue in the description
- Add tests when behaviour changes, and for media changes say how you verified it
- User facing changes update both the Chinese and English pages under `apps/docs`
- Keep the history clean and fold review fixups into the commit they belong to

## License

This project is AGPL-3.0-only. Opening a pull request means you agree your contribution ships under that same license and that you have the right to submit it. There is no contributor license agreement.

If your code came from another project or contains tool generated content, say so in the pull request along with the source and its license. Code whose origin cannot be established will not be merged.

## Code of conduct

Taking part means accepting the [code of conduct](./CODE_OF_CONDUCT.en.md).
