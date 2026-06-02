# llm-tap

A tiny OpenAI-compatible traffic tap for debugging LLM interactions.

It keeps the client request shape unchanged, forwards the request to the configured upstream, and writes readable traces to `./logs`.

## Quick start

```bash
cp config.example.yaml config.yaml
go run ./cmd/llm-tap
```

Configure your SDK base URL:

```text
http://127.0.0.1:8080/v1
```

Keep passing your API key from the client. llm-tap forwards `Authorization` but redacts it in logs.

## Configuration

```yaml
server:
  listen: "127.0.0.1:8080"

upstream:
  base_url: "https://api.openai.com"
  timeout_seconds: 120

logging:
  dir: "./logs"
  pretty_json: true
  expand_nested_json: true
```

llm-tap does not modify request bodies. It only forwards and records traffic.

## Logs

Each request creates a trace directory:

```text
logs/YYYY-MM-DD/trace_xxx/
  request.json
  response.json
  summary.md
```

`summary.md` extracts the model, messages, latency, stream flag, tool calls, tool results, and assistant output.
Set `logging.expand_nested_json` to `false` to keep JSON-encoded strings unchanged in summaries.
