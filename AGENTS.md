# AGENTS.md

Guidelines for AI coding agents working on the onWatch codebase.

## Build, Test & Lint Commands

```bash
# Run all tests (ALWAYS use -race before committing)
go test -race ./...

# Run a single test by name
go test ./internal/agent/ -run TestAgent_PollsAtInterval -v

# Build production binary
./app.sh --build

# Run full test suite with coverage
./app.sh --test

# Quick smoke test (vet + build check)
./app.sh --smoke

# Lint
make lint              # go fmt + go vet
go fmt ./...
go vet ./...

# Integration tests
go test -v -tags=integration ./...

# Dev mode (foreground, short interval)
go run . --debug --interval 10
```

**Mandatory pre-commit:** Run `go test -race ./...` and verify zero failures, zero race conditions.

## Code Style Guidelines

### Project Structure
- `internal/` - Private implementation packages
- `*_test.go` - Test files next to source files
- `main.go` - Entry point at repo root
- No root `static/` directory (use `internal/web/static/`)
- Temp files go in `temp/` subdirectories only

### Naming Conventions
- **Files:** `snake_case.go` (e.g., `anthropic_agent.go`)
- **Test files:** `foo.go` → `foo_test.go`
- **Functions:** `PascalCase` for exported, `camelCase` for private
- **Test names:** `TestComponent_Action_ExpectedResult`
  - Good: `TestAgent_PollsAtInterval`, `TestStore_SavesSnapshot_ReturnsID`
- **Variables:** Descriptive, avoid single-letter except for loops/receivers
- **Constants:** `UPPER_SNAKE_CASE` for exported, `camelCase` for private

### Imports
- Group: stdlib, third-party, internal
- No blank lines between groups in same category
- Example:
```go
import (
    "context"
    "fmt"
    "time"

    "modernc.org/sqlite"

    "github.com/onllm-dev/onwatch/internal/store"
)
```

### Error Handling
- Wrap errors with context: `fmt.Errorf("store.Save: %w", err)`
- Define package-level sentinel errors:
```go
var (
    ErrUnauthorized = errors.New("api: unauthorized")
    ErrNotFound     = errors.New("store: not found")
)
```
- Log errors at appropriate level: `slog.Error()`, `slog.Warn()`, `slog.Debug()`
- Check `ctx.Err()` for context cancellation

### Types & Structs
- Use struct tags for JSON: `json:"field_name,omitempty"`
- Prefer value types for small structs, pointers for large ones
- Use `time.Time` for timestamps, not strings

### Concurrency
- Always use `context.Context` for cancellation
- Use `sync/atomic` for counters shared across goroutines
- Use `sync.Mutex` for shared slices/maps in tests
- **CRITICAL for tests:** Never use bare `int`/`[]T` in `httptest` handlers - race detector will catch it
- Bounded buffers (limit cache to last 100 items max)

### Testing
- **TDD-first:** Write failing test, implement, verify pass, refactor
- Table-driven tests preferred:
```go
tests := []struct {
    name     string
    input    string
    want     int
    wantErr  bool
}{
    {"valid", "test", 42, false},
    {"empty", "", 0, true},
}
```
- Use `:memory:` SQLite for tests
- Use `httptest.NewServer` for API mocking
- Use `t.Cleanup()` for resource cleanup
- Use `atomic.Int32` for call counters in mock handlers
- Run `go test -race` before every commit

### Database (SQLite)
- Use `modernc.org/sqlite` (pure Go, no CGO)
- Single connection: `db.SetMaxOpenConns(1)`
- Small cache: `PRAGMA cache_size=-500` (512KB)
- WAL mode enabled
- Use parameterized queries (never string interpolation)

### Logging
- Use `log/slog` (stdlib)
- Structured: `slog.Info("poll complete", "requests", resp.Requests)`
- Redact secrets in logs (API keys, passwords)

### Security
- Never commit `.env` or database files
- Never log API keys
- Use `subtle.ConstantTimeCompare` for credential comparison
- Encrypt passwords with AES-GCM before storage
- Set HTTP timeouts (10s API, 5s shutdown)

### Comments
- Document exported functions, types, and packages
- Explain "why" not "what"
- Use `// TODO(username): description` for todos

### Commit Messages
- Conventional format: `feat:`, `fix:`, `test:`, `docs:`, `refactor:`, `perf:`
- Atomic commits - one logical change per commit
- Example: `feat: add Z.ai token limit tracking`

## Code Review Checklist
Before submitting changes:
1. `go test -race ./...` passes with zero races
2. `go vet ./...` shows no issues
3. Tests written/updated for new functionality
4. Error handling follows wrapping convention
5. No secrets or API keys in code
6. Comments added for exported items
