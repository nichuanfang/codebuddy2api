# Repository Guidelines

## Project Structure & Module Organization

This is a Go 1.25 gateway that exposes an OpenAI-compatible API backed by CodeBuddy.

- `main.go` — process entry point and CLI bootstrap.
- `api/` — HTTP server, route handlers, and middleware.
- `cli/` — Cobra commands, desktop launch integration, and platform helpers.
- `config/` — configuration types, Viper loading, and database/logging defaults.
- `core/` — database, Viper, and Zap initialization.
- `model/` — GORM models and migrations.
- `service/` — account selection, protocol conversion, SSE handling, sanitization, tasks, and most business logic.
- `task/` — background scheduling.
- `utils/` — shared helpers.
- `macos/`, root-level `.ps1`/`.vbs`/`.command` files — platform launch assets.
- `dist/` — generated release output; do not commit it.

Tests are co-located with the code under test as `*_test.go`.

## Build, Test, and Development Commands

```bash
go run . server          # run the gateway locally
go test ./...            # run every Go test
go fmt ./...             # format all Go source
make test                # wrapper for go test ./...
make dist                # rebuild and clean dist/
make clean               # remove dist/
```

Windows contributors can use `mingw32-make test` and `mingw32-make dist`. Building requires Go 1.25+, make, and a C compiler because SQLite uses CGO. Cross-compilation examples are in `README.md`; macOS binaries must be built on macOS.

## Coding Style & Naming Conventions

Use standard Go formatting: tabs, `gofmt`, and idiomatic naming (`MixedCase` for exported identifiers, `camelCase` for locals). Keep packages focused and prefer small, testable functions. Follow existing file prefixes such as `compat_`, `sanitize_`, and `task_` when adding related behavior.

## Testing Guidelines

Use Go's built-in testing package. Name files `*_test.go` and functions `TestXxx`; prefer table-driven tests and deterministic fakes over network calls. Add or update tests for protocol conversion, account rotation, sanitization, and configuration changes. Run `go test ./...` before committing.

## Commit & Pull Request Guidelines

Recent history uses Conventional Commits with scopes, for example `feat(Responses): ...`, `fix(兼容转换): ...`, and `ci(发布): ...`. Keep the subject in that style.

Pull requests should include a concise purpose statement, implementation notes, test evidence, and linked issues. Call out cross-platform behavior, configuration changes, and release-package impacts.

## Security & Configuration Tips

Never commit real `config.yaml`, `.env`, `data/`, logs, account exports, or credentials. Use `.env.example` and `config.yaml.example` as templates. Do not enable passwordless mode except for trusted loopback use.
## Agent-Specific Instructions

- The current Codex session depends on the gateway running from `dist/`. Never kill, stop, restart, overwrite, or delete that process or its executable.
- Do not run `make dist` or `make clean` for routine changes; they rebuild or remove `dist/`. Build to another location or run a separate development instance on a different port instead.
- Performance optimization and bug fixes must preserve existing behavior. Cover affected paths with regression tests and run `go test ./...` before delivery.