This repository is a Matrix bridge for TikTok messaging. Most work falls into one of two areas:

- `pkg/libtiktok`: low-level TikTok client code, HTTP/WebSocket transport, reverse-engineered wire formats
- `pkg/connector`: bridge logic that converts TikTok events/messages into Matrix-facing behavior

## Fast Discovery

- Start in `pkg/libtiktok` if the issue mentions TikTok requests, protobufs, cookies, WebSocket events, message send/fetch, reactions, or auth headers.
- Start in `pkg/connector` if the issue mentions Matrix behavior, user mapping, message conversion, login flow, or bridge orchestration.
- The CLI entrypoints live under `cmd/`.

## Protobuf Layout

- Reverse-engineered protobuf schemas live in `proto/tiktok/im/v1/im.proto`.
- Generated Go types live in `pkg/libtiktok/pb/`. Treat that directory as generated code; edit the `.proto` file instead.
- Small handwritten adapters/helpers live in `pkg/libtiktok/protobuf_helpers.go`.
- When changing the wire schema, keep the generated types as the wire layer and convert into the handwritten domain types (`Conversation`, `Message`, `Reaction`) rather than spreading generated types through the rest of the codebase.

## Message Flow

- Inbox/history/send/reaction REST flows are centered in `pkg/libtiktok/inbox.go` and `pkg/libtiktok/messages.go`.
- Real-time inbound chat events are parsed in `pkg/libtiktok/websocket.go`.
- Message body content is not fully protobuf: the protobuf carries a JSON blob that is interpreted by `parseMessageContent()`.

## Working With Reverse-Engineered Protocols

- Prefer conservative changes. Field numbers and partially understood fields should stay stable unless you have strong evidence from captures or live behavior.
- Keep protocol intent documented close to the schema. If you learn something durable about a field, add it to `im.proto`.
- Preserve unknown-field tolerance and avoid "cleanup" renames that make the schema sound more certain than it is.

## Common Conventions

- Shared request envelopes are reused across multiple TikTok IM endpoints; look for the typed inner payload rather than assuming each endpoint is fully unique.
- Metadata ordering can matter for TikTok web requests, especially around authenticated send/reaction flows.
- `Conversation.SourceID` is important for follow-up operations like history fetch and reactions.
- `ClientMessageID` is the most useful stable logical message key across REST and WebSocket paths.

## Verification

- If you change protobuf schemas, regenerate code before testing.
- Do not hand-edit generated `.pb.go` files.
