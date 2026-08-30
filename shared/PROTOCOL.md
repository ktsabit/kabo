# Cambio wire protocol

The browser sends JSON action intents over `GET /ws`; the Go server validates every action and answers with a player-specific `snapshot`. Hidden card values are omitted, not merely covered in the UI.

Connections identify a room and player with either an authenticated Discord session (`?room=<instanceId>&session=<opaque id>`) or, in guest development mode, `?room=<room>&user=<id>&name=<name>`.

The TypeScript definitions in `protocol.ts` are the canonical browser contract. Matching Go wire structs live in `server/game/protocol.go`. Slaps include `discardEventId`; the first valid request changes that ID atomically, making every later request against the previous discard stale.

