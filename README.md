# Kabo Discord Activity MVP

A playable, server-authoritative Kabo card game. It runs as a normal web app for local development and uses the same React client inside a Discord Activity.

## What is implemented

- Two to eight active players in in-memory rooms, with read-only spectators who can queue for the next round.
- Four face-down cards per player in a stable two-row hand that expands to the right and closes same-row gaps with a visible horizontal slide.
- Draw, replace, or discard turns.
- 7/8 own peek, 9/10 opponent peek, J/Q any-two-card swap, and K opponent peek followed by any-two-card swap.
- Server-ordered slap races for every new discard. The first valid slap wins; later correct slaps show the attempted card to everyone without a penalty, while wrong slaps add one penalty card.
- Wrong slap penalty cards and the opponent-card slap/gift flow.
- Immediate round endings for an exhausted pile, an empty hand, or a call of Kabo.
- Full scoring, including Jokers at 0 and red Kings at −1.
- Private per-player snapshots: unrevealed card values never reach other browsers.
- Discord OAuth code exchange, opaque game sessions, Activity `instanceId` rooms, and optional Activity Instance API validation.
- Reconnect support for the same player identity, recovery from interrupted animations, disconnected-turn skipping before draw, and ready-gated rematches.
- Mid-round spectators, a compact next-round roster capped at eight, and automatic promotion into the next round.
- Lobby readiness checks for every selected player, with the previous round's winner starting the next round.
- Configurable server deadlines: 30 seconds for the opening peek, 15 seconds for turn phases, and 3 seconds for reveal acknowledgement by default; a drawn card is discarded automatically when possible, otherwise the turn advances.
- SQLite audit history with Activity/room metadata (application, instance, guild, channel, location, platform, and launch identifiers), player scores and outcomes, and a chronological per-round event log.
- An optional Discord `/leaderboard` slash command, ranked by total round wins within the current server.
- Flowing power/discard indicators, queued slap animations, face-up late/wrong slap flights, and a responsive table shell for narrow or short Discord viewports.

Card faces use the CC0-licensed [`@letele/playing-cards`](https://github.com/letele/playing-cards) SVG deck, based on Adrian Kennard's classic designs. The custom indigo bear artwork remains the card back.

## Repository layout

```text
client/             React + TypeScript + Vite table UI
server/             Go HTTP/WebSocket server and game state machine
server/game/        Authoritative rules and tests
shared/             Browser protocol types and wire-protocol notes
Dockerfile          Single-origin production image
```

## Run locally

Requirements: Node 20.19+ (Node 24 works) and Go 1.24+.

```bash
cd server
ALLOW_GUESTS=true go run .
```

In another terminal:

```bash
cd client
npm install
npm run dev
```

Open `http://localhost:5173/?room=friends&name=Ada`. To simulate another player, use a different browser profile or add a distinct `user` query parameter.

Run verification with:

```bash
npm --prefix client run build
npm --prefix client test
npm --prefix client run test:responsive
cd server && go test ./...
cd server && go test ./game -run=^$ -fuzz=FuzzRapidOverlappingActions -fuzztime=5s
```

## Production deployment

The repository includes a single-origin Docker image that builds the React client, compiles the Go server, serves both on one port, disables browser guests by default, and exposes `/healthz` for the host's health check.

Run the production image with Docker Compose:

```bash
cp .env.example .env
# Fill in the Discord values in .env on the VPS, then set ALLOW_GUESTS=false for production.
docker compose up --build -d
```

The app is available at `http://localhost:8080`. Stop it with `docker compose down`. Round results persist in the `kabo-data` volume, while live rooms and sessions remain in memory.

The Activity reads the public Discord Application ID from the running server, so it does not need to be baked into the browser bundle. A direct Docker build is enough:

```bash
docker build -t kabo .
```

Run the container with server-only secrets:

```bash
docker run --rm -p 8080:8080 \
  -e DISCORD_CLIENT_ID=YOUR_DISCORD_APPLICATION_ID \
  -e DISCORD_CLIENT_SECRET=YOUR_DISCORD_CLIENT_SECRET \
  -e DISCORD_BOT_TOKEN=YOUR_DISCORD_BOT_TOKEN \
  -e DISCORD_PUBLIC_KEY=YOUR_DISCORD_PUBLIC_KEY \
  kabo
```

Choose a Docker host that provides a public HTTPS domain, forwards WebSocket upgrades, keeps one process alive, and sends traffic to port 8080. Keep this MVP at exactly **one replica** with no scale-to-zero: live rooms and sessions are in memory, so restarts erase active games and multiple replicas would split players unless Redis or a durable room service is added first. Set `DB_PATH`, `KABO_INITIAL_TIMEOUT`, `KABO_TURN_TIMEOUT`, and `KABO_REVEAL_TIMEOUT` in Compose when needed.

### Cloudflare hosting

The current Go server cannot run as a normal Cloudflare Worker. Cloudflare Containers can run this Docker image, but Containers require the Workers Paid plan. A genuinely free Cloudflare deployment would require replacing the Go room server with a Worker plus one SQLite-backed Durable Object per Activity instance and using the Hibernation WebSocket API. The React client and wire protocol can remain; the authoritative room implementation would be a backend migration.

## Discord Activity setup (current SDK flow)

1. Create an application in the [Discord Developer Portal](https://discord.com/developers/applications), enable Activities, and keep the automatically-created **Launch** Entry Point command. Discord currently creates this command when Activities are enabled.
2. Under Installation, enable both User Install and Guild Install. Under Activity Settings, select every platform you intend to test (desktop, web, iOS, and/or Android). Under OAuth2, add `https://127.0.0.1` as the placeholder redirect URI; the Embedded App SDK handles the Activity redirect.
3. Put `DISCORD_CLIENT_ID` and `DISCORD_CLIENT_SECRET` in the root `.env` beside `docker-compose.yml` on the VPS. The client obtains the public Application ID from `/api/config`; the client secret never reaches Vite or the browser.
4. Serve the built client and Go API from the same public HTTPS origin. For development, tunnel port 8080 after building the client, or tunnel the Vite port while separately mapping the API. In **Activities → URL Mappings**, map prefix `/` to the public hostname **without** `https://`.
5. Set `ALLOW_GUESTS=false` in production. Set `DISCORD_BOT_TOKEN` to make the backend validate the supplied instance through Discord's Activity Instance API before creating a game session.

### Discord `/leaderboard` command

The server includes a signed HTTP interaction handler at `/api/discord/interactions` and reads completed Discord Activity rounds from the same SQLite database. To enable the command:

1. Copy **General Information → Public Key** into `DISCORD_PUBLIC_KEY`.
2. Set `DISCORD_GUILD_ID` to a test server ID and `DISCORD_REGISTER_COMMANDS=true`, then restart once. The server registers `/leaderboard` as a guild command, which Discord updates immediately.
3. Set `DISCORD_REGISTER_COMMANDS=false` after registration. Remove `DISCORD_GUILD_ID` and repeat registration if you want a global command; global command propagation can take longer.
4. In **General Information → Interactions Endpoint URL**, enter `https://YOUR_DOMAIN/api/discord/interactions`.
5. Install the application in the server with the `applications.commands` scope, then run `/leaderboard` in that server.

The command is public and ranks the server by total round wins. Its image uses a top-three podium followed by compact ranked rows; the requesting member is highlighted under their full server nickname. If more than ten players are ranked, owner-only Previous and Next buttons page through the full standings without changing absolute ranks. An owner-only red ❌ button removes the message. Ties are broken by win rate, then average hand score and rounds played. Older Activity rows that stored the Discord client as `desktop` or `mobile` are migrated automatically to the `discord` round source while retaining the client-platform detail. The endpoint verifies Discord's `X-Signature-Ed25519` and `X-Signature-Timestamp` headers before reading any interaction.

The client follows the official flow: construct `DiscordSDK`, wait for `ready()`, request `identify` and `applications.commands`, exchange the authorization code on `/api/token`, call `authenticate`, and use `sdk.instanceId` as the room key. Inside the proxy it uses `/.proxy/api/token` and `/.proxy/ws`; normal browser mode uses `/api/token` and `/ws`.

The code integration is Activity-ready. Launch readiness still requires: real portal credentials, a public HTTPS deployment, the `/` URL mapping, supported-platform selections, and one successful test launch from Discord's Developer Activity Shelf. The current SDK dependency is `@discord/embedded-app-sdk` 2.5.0.

Credential locations in the Developer Portal:

- `DISCORD_CLIENT_ID`: **General Information → Application ID** (also shown as the OAuth2 Client ID). This value is public.
- `DISCORD_CLIENT_SECRET`: **OAuth2 → Client Secret**. This is private and belongs only in the host's runtime secrets.
- `DISCORD_BOT_TOKEN`: **Bot → Token → Reset Token** if Discord is not currently showing one. This is private and belongs only in the host's runtime secrets.
- `DISCORD_PUBLIC_KEY`: **General Information → Public Key**. Used to verify signed Discord interactions; it is not a secret.
- `DISCORD_GUILD_ID`: Optional test-server ID for registering `/leaderboard` as an instant guild command.
- `DISCORD_REGISTER_COMMANDS`: Set to `true` only while registering the command; leave it `false` afterward.

Discord Activities route network traffic through their proxy. WebSockets are supported; WebRTC is not. Because this app keeps assets, OAuth, and WebSocket traffic on one mapped origin, no extra third-party URL mappings are required. See Discord's current [Activity tutorial](https://docs.discord.com/developers/activities/building-an-activity), [networking guide](https://docs.discord.com/developers/activities/development-guides/networking), and [multiplayer/instance guide](https://docs.discord.com/developers/activities/development-guides/multiplayer-experience).

## MVP rule decisions

- A special power triggers only when the drawn special card is discarded. Replacing one of your cards with it does not trigger the power.
- Calling Kabo is allowed at the start of your own turn and ends the round immediately.
- K's peek and swap are both mandatory; the peeked card may be one side of the swap.
- J/Q/K swaps require two different occupied slots, but both slots may belong to the same player.
- A successful slap wins the race for that discard and closes its slap slot. A later correct slap is revealed but not penalized; only a wrong rank or empty slot draws a penalty. If the successful slap targets an opponent, that gift is resolved after the race is already closed.
- The active turn is completed before an empty draw pile ends the round, except when a penalty needs a card and none remains.
- A round starts only when every selected player is connected and ready; the previous winner gets the first turn when they join the next round.
- A failed Kabo call marks the caller as the loser even if another player has a higher score; the lowest score still determines the winner. A caller tied for the lowest score succeeds.

## Deliberately deferred

Persistent live rooms across server restarts, a timed slap window, moderation controls, telemetry, and horizontal scaling are post-MVP work. Moving rooms to Redis (or a durable room actor) is the natural next backend step; SQLite currently preserves completed round results only.
