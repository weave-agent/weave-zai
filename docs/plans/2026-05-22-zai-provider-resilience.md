# Z.ai Provider Resilience

## Overview
Wire the Z.ai extension into shared provider HTTP and retry SDK support. This removes the production bare HTTP client, applies global/provider-specific transport and retry config, and passes explicit retry policy to `utils/openaicompat` while preserving Z.ai-specific request behavior.

## Context (from discovery)
- Files/components involved:
  - `zai.go`
  - `zai_test.go`
  - root SDK packages `sdk/providerhttp`, `sdk/providerretry`, and `utils/openaicompat`
- Related patterns found:
  - Z.ai currently creates `&http.Client{}` with no explicit timeout
  - Z.ai uses `openaicompat.Stream`
  - Z.ai adds `tool_stream: true` and custom thinking mutation
- Dependencies identified:
  - `github.com/weave-agent/weave/sdk/providerhttp`
  - `github.com/weave-agent/weave/sdk/providerretry`
  - `github.com/weave-agent/weave/sdk/retry`

## Development Approach
- **Testing approach**: Regular
- Complete each task fully before moving to the next
- Make small, focused changes
- Every task that changes code includes new or updated tests
- All tests must pass before starting the next task
- Update this plan file when scope changes during implementation
- Maintain backward compatibility for existing provider config fields

## Testing Strategy
- Provider initialization tests for default and custom HTTP/retry config
- Invalid provider config tests
- Stream tests updated for new `openaicompat` signature
- Run `go test ./...` in this repo after each task

## Progress Tracking
- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document issues/blockers with ⚠️ prefix
- Keep plan in sync with implementation

## What Goes Where
- Implementation Steps contain automatable code/test/doc tasks
- Post-Completion contains manual verification and external coordination
- Checkboxes belong only in task sections

## Implementation Steps

### Task 1: Resolve provider runtime at initialization
- [x] update `zai.go` to resolve `providerhttp.ForProvider(cfg, "zai")`
- [x] update `zai.go` to resolve `providerretry.ForProvider(cfg, "zai")`
- [x] store HTTP client and retry config in provider state
- [x] preserve existing API key, model, base URL, extra body, and thinking mutation behavior
- [x] write tests for provider init with custom HTTP/retry config
- [x] write tests for invalid HTTP/retry config failing provider init
- [x] run `go test ./...` - must pass before next task

### Task 2: Pass explicit retry config to OpenAI-compatible stream
- [x] update `Stream` call to pass configured retry policy to `openaicompat.Stream`
- [x] update tests for any changed `openaicompat.Stream` signature
- [x] verify no production path creates bare `&http.Client{}`
- [x] write tests proving stream uses configured runtime where practical
- [x] run `go test ./...` - must pass before next task

### Task 3: Verify acceptance criteria
- [ ] verify default provider config still works
- [ ] verify provider-specific HTTP override is accepted
- [ ] verify provider-specific retry override is accepted
- [ ] verify invalid provider config returns clear initialization error
- [ ] run `go test ./...`

## Technical Details
Provider runtime shape:

```go
type provider struct {
    client *http.Client
    retry  retry.Config
    config openaicompat.ProviderConfig
}
```

Z.ai should not parse HTTP/retry config itself; use SDK resolvers.

## Post-Completion
Manual verification:
- Run Z.ai with default provider config
- Run Z.ai with provider-specific HTTP/retry overrides
- Confirm retry debug logs do not contain secrets or prompts
