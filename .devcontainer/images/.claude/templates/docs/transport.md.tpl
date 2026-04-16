# Transport

Communication protocols and data exchange formats used by **{{PROJECT_NAME}}**.

## Protocols

| Protocol | Direction | Port | TLS | Used by |
|----------|-----------|------|:---:|---------|
<!-- FOR EACH detected transport protocol -->
<!-- | HTTP/HTTPS | Request/Response | 443 | :white_check_mark: | [HTTP API](api/http-api.md) | -->
<!-- | WebSocket | Bidirectional | 443 | :white_check_mark: | [Realtime API](api/realtime.md) | -->
<!-- | gRPC | RPC | 50051 | :white_check_mark: | [Internal RPC](api/internal-rpc.md) | -->

## Exchange Formats

| Format | Content-Type | Used by | Detection |
|--------|-------------|---------|:---------:|
<!-- FOR EACH detected exchange format -->
<!-- | JSON | application/json | [HTTP API](api/http-api.md) | Explicit | -->
<!-- | Protobuf | application/grpc | [Internal RPC](api/internal-rpc.md) | Deduced | -->

!!! info "Auto-detection"
    Formats are detected from source code imports and dependencies.
    Formats marked **Deduced** are inferred from protocol conventions
    (e.g., gRPC → Protobuf, REST handler with `json.Marshal` → JSON).

## Protocol Details

<!-- FOR EACH transport protocol, generate a subsection -->
<!-- ### HTTP/HTTPS -->
<!-- Description of how HTTP is used in this project -->
<!-- Optional Mermaid sequence diagram for the primary HTTP flow -->
