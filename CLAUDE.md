# CLAUDE.md

## Provider Runtime Configuration

The Z.ai provider resolves HTTP and retry behavior during provider initialization through the shared weave SDK helpers:

- `providerhttp.ForProvider(cfg, "zai")`
- `providerretry.ForProvider(cfg, "zai")`

Production provider traffic must use the resolved `*http.Client`; do not create bare production `&http.Client{}` instances. Store the resolved retry policy in `openaicompat.ProviderConfig.RetryConfig` before calling `openaicompat.Stream` or the tokenizer endpoint.

Preserve Z.ai-specific request behavior: include `tool_stream: true`, and when thinking is enabled set `enable_thinking` and remove `reasoning_effort`.

`CountTokens` calls `${TokenizerBaseURL}/tokenizer` with auth and extra headers, applies the shared thinking request mutation, and omits streaming-only request fields such as `tool_stream`. Histories containing prior assistant tool calls or tool results return an unsupported-message error before calling the endpoint, because Z.ai's tokenizer schema does not document those OpenAI-compatible message forms.
