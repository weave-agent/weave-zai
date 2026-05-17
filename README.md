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
