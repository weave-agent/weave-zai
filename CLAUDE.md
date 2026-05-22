# CLAUDE.md

## Provider Runtime Configuration

The Z.ai provider resolves HTTP and retry behavior during provider initialization through the shared weave SDK helpers:

- `providerhttp.ForProvider(cfg, "zai")`
- `providerretry.ForProvider(cfg, "zai")`

Production provider traffic must use the resolved `*http.Client`; do not create bare production `&http.Client{}` instances. Store the resolved retry policy in `openaicompat.ProviderConfig.RetryConfig` before calling `openaicompat.Stream`.

Preserve Z.ai-specific request behavior: include `tool_stream: true`, and when thinking is enabled set `enable_thinking` and remove `reasoning_effort`.
