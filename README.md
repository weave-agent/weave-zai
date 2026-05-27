# weave-zai

Z.ai provider extension for [weave](https://github.com/weave-agent/weave) — an event-driven coding agent framework.

## Fork & Customize

1. Fork this repo
2. Edit the extension implementation
3. Install your fork: `weave install github.com/<you>/weave-zai --name zai`

The `--name zai` ensures your fork shadows the official extension.

## Install

```bash
weave install github.com/weave-agent/weave-zai --name zai
```

## Configuration

The provider reads `ZAI_API_KEY` for auth. It also supports optional `ZAI_MODEL` and `ZAI_BASE_URL` environment overrides.

Z.ai uses the shared weave provider HTTP and retry settings. Defaults can be configured under `providers.defaults`; Z.ai-specific overrides go under `providers.zai`.

```json
{
  "providers": {
    "zai": {
      "model": "glm-5.1",
      "base_url": "https://api.z.ai/api/coding/paas/v4",
      "tokenizer_base_url": "https://api.z.ai/api/paas/v4",
      "http": {
        "dial_timeout": "10s",
        "tls_handshake_timeout": "10s",
        "response_header_timeout": "60s",
        "idle_conn_timeout": "90s"
      },
      "retry": {
        "max_retries": 5,
        "base_delay": "1s",
        "max_delay": "30s",
        "multiplier": 2,
        "jitter": "full"
      }
    }
  }
}
```

Duration values use Go duration strings such as `250ms`, `2s`, or `1m`. Retry jitter accepts `full` or `none`.

## Token Accounting

Z.ai reports generation usage through the OpenAI-compatible streaming response. When the provider includes compatible usage details, weave maps prompt tokens, completion tokens, and cached prompt tokens into provider usage telemetry.

For preflight context budgeting, this extension implements input-token counting with Z.ai's `/tokenizer` endpoint when the selected model is a registered text model also accepted by the tokenizer endpoint: `glm-4.6` or `glm-4.5`. The default chat model is currently `glm-5.1`, which Z.ai does not document as a tokenizer model, so `CountTokens` returns an unsupported-model error for the default instead of sending the request or falling back to a heuristic estimate.

The count request uses the same model, messages, tools, system prompt, and thinking-mode request mutation as chat streaming. The tokenizer path does not add streaming-only options such as `tool_stream`. Tool result messages are converted into tokenizer-supported text messages so conversations after tool execution remain countable.

Tokenizer counts are reported as input tokens with source `tokenizer`; output tokens are not estimated during preflight counting. If `/tokenizer` is unsupported for the selected model, fails, or returns a malformed response, `CountTokens` returns that error instead of falling back to a heuristic count.

The default `glm-5.1` model metadata advertises a 204800-token context window and 131072 max output tokens for budget decisions.

## Development

```bash
git clone git@github.com:weave-agent/weave-zai.git
cd weave-zai

# Add temporary replace for local SDK (don't commit this)
echo 'replace github.com/weave-agent/weave => /path/to/local/weave' >> go.mod

go test ./...
```

## License

Same as the main weave project.
