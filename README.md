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
