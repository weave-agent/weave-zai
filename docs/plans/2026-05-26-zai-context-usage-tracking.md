# Improve Z.ai Context Usage Tracking

## Overview
- Improve context and usage tracking for the Z.ai OpenAI-compatible provider.
- Consume shared OpenAI-compatible cached-token parsing once available in the root repo.
- Document and implement honest token count support based on what the provider API supports.

## Context (from discovery)
- Files/components involved:
  - `zai.go`
  - `zai_test.go`
  - `models.go`
  - root repo `utils/openaicompat` shared transport
- Related patterns found:
  - Z.ai delegates streaming to `utils/openaicompat.Stream`.
  - Provider adds `tool_stream: true` to the request body.
  - Thinking mode uses provider-specific `enable_thinking` and removes `reasoning_effort`.
  - Model metadata includes context windows and max output tokens.
- Dependencies identified:
  - Root repo SDK and OpenAI-compatible usage parsing changes should land first.
  - Exact count support depends on Z.ai API availability.

## Development Approach
- **Testing approach**: Regular (code first, then tests)
- Complete each task fully before moving to the next.
- Make small, focused changes.
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task.
- **CRITICAL: all tests must pass before starting next task** - no exceptions.
- **CRITICAL: update this plan file when scope changes during implementation**.
- Run tests after each change.
- Maintain backward compatibility.

## Testing Strategy
- **Unit tests**: required for every task.
- **E2E tests**: not expected for provider accounting changes.

## Progress Tracking
- Mark completed items with `[x]` immediately when done.
- Add newly discovered tasks with ➕ prefix.
- Document issues/blockers with ⚠️ prefix.
- Update plan if implementation deviates from original scope.
- Keep plan in sync with actual work done.

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): tasks achievable within this codebase - code changes, tests, documentation updates.
- **Post-Completion** (no checkboxes): items requiring external action - manual testing, changes in consuming projects, deployment configs, third-party verifications.
- **Checkbox placement**: Checkboxes belong only in Task sections.

## Implementation Steps

### Task 1: Consume shared OpenAI-compatible usage improvements
- [x] update dependency on root repo `utils/openaicompat` cached-token parsing changes (already on `github.com/weave-agent/weave v0.0.11`, which includes cached-token parsing)
- [x] verify `sdk.ProviderUsage.CacheReadTokens` is populated when Z.ai sends compatible details
- [x] preserve `tool_stream` request behavior
- [x] write tests for normal usage event mapping
- [x] write tests for cached-token usage detail when provider sends it
- [x] run `go test ./...` - must pass before next task

### Task 2: Evaluate provider count-token support
- [x] verify whether Z.ai exposes a compatible token count endpoint or deterministic tokenizer guidance
- [x] implement `sdk.TokenCounter` only if a supported count mechanism exists
- [x] N/A: unsupported-tokenizer fallback documentation was not needed because Z.ai exposes `/tokenizer`
- [x] write tests for count success/error path or unsupported fallback behavior
- [x] write tests preserving thinking request modifications in any count path
- [x] run `go test ./...` - must pass before next task

### Task 3: Verify model metadata for budget decisions
- [x] review Z.ai model `ContextWindow` and `MaxTokens` values against current provider docs
- [x] update model metadata only with verified current values
- [x] write tests for default model and metadata registration
- [x] write tests for thinking/reasoning capability expectations
- [x] run `go test ./...` - must pass before next task

### Task 4: Verify acceptance criteria
- [x] verify context usage telemetry is richer where provider data is available
- [x] verify no unsupported exact-count claims are exposed
- [x] run full provider tests with `go test ./...`
- [x] ran `golangci-lint run ./...` successfully on 2026-05-27
- [x] verify no prompts or API keys are logged in accounting paths

### Task 5: Update documentation
- [x] update README or provider docs with Z.ai token accounting support level

## Technical Details
- Keep provider-specific request body modifications centralized in `ModifyRequest`.
- Do not add fake exact counting; return exact only when supported by provider or tokenizer with known model encoding.
- Shared OpenAI-compatible parsing should remain in root `utils/openaicompat`.

## Post-Completion

**Manual verification**:
- Run a Z.ai session and verify context and cache telemetry displays correctly.

**External system updates**:
- Agent repo should consume richer usage through shared SDK events.
