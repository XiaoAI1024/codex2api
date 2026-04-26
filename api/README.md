# Documentation Index

## API Documentation

### OpenAPI/Swagger Specification

The complete OpenAPI 3.0.3 specification is available in [`openapi.yaml`](./openapi.yaml).

### Error Codes Reference

See [`errors.md`](./docs/errors.md) for detailed error code documentation.

### API Versioning

This API uses URL path versioning (e.g., `/v1/`). The current version is v1.0.0.

### Authentication

All API endpoints require authentication via Bearer token in the Authorization header:

```
Authorization: Bearer <API_KEY>
```

### Upstream Header Compatibility

- `Version` is not synthesized by default. Codex2API forwards it only when the downstream client explicitly sends `Version: <CODEX_CLI_VERSION>`.
- Responses WebSocket requests add the required WebSocket beta header unless the client already sends an `OpenAI-Beta` value that contains `responses_websockets=`.

### Response Formats

#### Success Response

```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1234567890,
  "model": "gpt-5.4",
  "choices": [...],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30
  }
}
```

#### Error Response

```json
{
  "error": {
    "code": "invalid_request",
    "message": "Invalid request",
    "type": "invalid_request_error",
    "details": {
      "field": "model",
      "message": "Model 'invalid-model' is not supported"
    }
  }
}
```

### Rate Limiting

Rate limits are returned in response headers:

- `X-RateLimit-Limit`: Maximum requests allowed
- `X-RateLimit-Remaining`: Remaining requests
- `X-RateLimit-Reset`: Unix timestamp when the limit resets

### Supported Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/models` | GET | List available models (root-path alias: `/models`) |
| `/v1/chat/completions` | POST | Create chat completion (root-path alias: `/chat/completions`) |
| `/v1/responses` | POST | Create response (Codex native, root-path alias: `/responses`) |
| `/v1/responses` | GET | Responses WebSocket transport for Codex CLI compatibility (root-path alias: `/responses`) |
| `/v1/responses/compact` | POST | Create compact response through Codex upstream `/responses/compact`; rejects `stream:true` (root-path alias: `/responses/compact`) |
| `/backend-api/codex/responses` | POST | Codex CLI direct compatibility alias for `/v1/responses` |
| `/backend-api/codex/responses` | GET | Codex CLI direct WebSocket alias for `GET /v1/responses` |
| `/backend-api/codex/responses/compact` | POST | Codex CLI direct compatibility alias for `/v1/responses/compact` |
| `/v1/images/generations` | POST | Generate images (root-path alias: `/images/generations`) |
| `/v1/images/edits` | POST | Edit images with JSON or multipart form data (root-path alias: `/images/edits`) |
| `/health` | GET | Health check |

If your client uses a `base_url` without `/v1`, the same OpenAI-compatible endpoints are also available on the root paths listed above. Codex CLI style `chatgpt_base_url` clients may use the `/backend-api/codex/*` direct aliases.

### Images Compatibility

`/v1/images/generations` and `/v1/images/edits` align with CLIProxyAPI image behavior:

- Default image tool model is `gpt-image-2`.
- Generation accepts JSON payloads with `prompt`, `model`, `size`, `quality`, `background`, `output_format`, `output_compression`, `moderation`, `partial_images`, `response_format`, and `stream`.
- Edits accept JSON payloads or `multipart/form-data`; multipart supports `image`, repeated `image[]`, `mask`, `prompt`, `model`, `input_fidelity`, and the same output options as generations.
- `response_format` can be `b64_json` or `url`. URL mode may return a data URL.
- `stream:true` returns `text/event-stream` with events such as `image_generation.partial_image`, `image_generation.completed`, `image_edit.partial_image`, and `image_edit.completed`, followed by `data: [DONE]`.

JSON generation example:

```bash
curl -X POST "<BASE_URL>/v1/images/generations" \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "Create a clean dashboard hero illustration",
    "size": "1024x1024",
    "response_format": "b64_json"
  }'
```

Multipart edit example:

```bash
curl -X POST "<BASE_URL>/v1/images/edits" \
  -H "Authorization: Bearer <API_KEY>" \
  -F "model=gpt-image-2" \
  -F "prompt=Keep the subject and replace the background" \
  -F "image=@/path/to/input.png" \
  -F "mask=@/path/to/mask.png" \
  -F "input_fidelity=high" \
  -F "response_format=url"
```

Streaming image example:

```bash
curl -N -X POST "<BASE_URL>/v1/images/generations" \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "Create a small app icon",
    "partial_images": 2,
    "stream": true
  }'
```

Example SSE payload:

```text
data: {"type":"image_generation.partial_image","partial_image_index":0,"b64_json":"<BASE64_IMAGE_CHUNK>"}

data: {"type":"image_generation.completed","b64_json":"<BASE64_IMAGE>","usage":{"total_images":1}}

data: [DONE]
```

### Responses WebSocket and Built-in Tools

`GET /v1/responses`, `GET /responses`, and `GET /backend-api/codex/responses` expose the Codex CLI compatible WebSocket transport. Clients send a JSON text frame using either a `response.create` envelope or a plain Responses API request:

```json
{
  "type": "response.create",
  "model": "gpt-5.4",
  "input": "Hello",
  "stream": true
}
```

The server forwards upstream response events as WebSocket text frames. Built-in Responses tool aliases are normalized before forwarding; for example, `web_search_preview` and `web_search_preview_2025_03_11` become `web_search` in `tools`, `tool_choice`, and `tool_choice.tools`.

### Model Support

Supported models include:
- `gpt-5.4`
- `gpt-5.4-mini`
- `gpt-5`
- `gpt-5-codex`
- `gpt-5-codex-mini`
- `gpt-5.1`, `gpt-5.1-codex`, etc.

See the OpenAPI spec for the complete list.
