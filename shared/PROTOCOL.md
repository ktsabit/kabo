# Cambio wire protocol

The browser sends JSON action intents over `GET /ws`; the Go server validates every action and answers with a player-specific `snapshot`. Hidden card values are omitted, not merely covered in the UI.

Connections identify a room and player with either an authenticated Discord session (`?room=<instanceId>&session=<opaque id>`) or, in guest development mode, `?room=<room>&user=<id>&name=<name>`.

The TypeScript definitions in `protocol.ts` are the canonical browser contract. Matching Go wire structs live in `server/game/protocol.go`. Slaps include `discardEventId`; the first valid request changes that ID atomically, making every later request against the previous discard stale.

When a new identity connects while a round is active, the server keeps the connection as a spectator. Spectators receive the public board but no private card values, cannot submit game actions, and may send `{"type":"set_next_round","joinNextRound":true|false}`. `nextRoundPlayers` is the server-capped roster of at most eight players; `waitingPlayers` reports the remaining waiting identities and their queue choice. In `lobby` or `ended`, selected players may send `{"type":"set_ready","ready":true|false}`; `allReady` gates `start_game`. `nextStarterId` identifies the previous round's winner, who starts the next round when queued. Existing active identities reconnect to their current seat instead of entering the waiting roster.
