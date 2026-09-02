# Cambio wire protocol

The browser sends JSON action intents over `GET /ws`; the Go server validates every action and answers with a player-specific `snapshot`. Hidden card values are omitted, not merely covered in the UI.

Connections identify a room and player with either an authenticated Discord session (`?room=<instanceId>&session=<opaque id>`) or, in guest development mode, `?room=<room>&user=<id>&name=<name>`.

The Discord token exchange receives the Activity's non-secret context (`instanceId`, `guildId`, `channelId`, `locationId`, client platform, and launch identifiers) and carries it into the room's SQL audit record. The round source is stored as `discord`; the SDK's `desktop`/`mobile` value is stored separately as the client platform. OAuth codes, access tokens, and opaque session IDs are not persisted in the database.

The TypeScript definitions in `protocol.ts` are the canonical browser contract. Matching Go wire structs live in `server/game/protocol.go`. Slaps include `discardEventId`; the first valid request changes that ID atomically, making every later request against the previous discard stale. Discard and action cursors remain monotonic for the lifetime of a room, including across rounds.

When a new identity connects while a round is active, the server keeps the connection as a spectator. Spectators receive the public board but no private card values, cannot submit game actions, and may send `{"type":"set_next_round","joinNextRound":true|false}`. `nextRoundPlayers` is the server-capped roster of at most eight players; `waitingPlayers` reports the remaining waiting identities and their queue choice. In `lobby` or `ended`, selected players may send `{"type":"set_ready","ready":true|false}`; `allReady` gates `start_game`. `nextStarterId` identifies the previous round's winner, who starts the next round when queued. Existing active identities reconnect to their current seat instead of entering the waiting roster.

Slap requests are serialized by the room lock and carry the current `discardEventId`; the first valid request advances that ID and wins the single slap slot for that discard. A later correct request, including one after the top card has itself been slapped, publishes a transient `action` with `kind: "late_slap"` and the attempted card without changing game state or adding a penalty. A wrong rank or empty target publishes `kind: "wrong_slap"` and adds a penalty card. In both cases the attempted card is revealed publicly, while it remains omitted from the target player's normal private snapshot.
