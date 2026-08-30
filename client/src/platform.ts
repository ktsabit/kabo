import type { DiscordSDK as DiscordSDKType } from "@discord/embedded-app-sdk";

export interface PlatformIdentity {
  id: string;
  name: string;
}

export interface PlatformSession {
  mode: "browser" | "discord";
  identity: PlatformIdentity;
  roomId: string;
  session?: string;
  apiPrefix: string;
  participants?: number;
}

function clean(value: string | null, fallback: string): string {
  const trimmed = value?.trim();
  return trimmed ? trimmed.slice(0, 40) : fallback;
}

function randomID(): string {
  return crypto.randomUUID().replaceAll("-", "");
}

function isDiscordFrame(): boolean {
  const params = new URLSearchParams(window.location.search);
  return Boolean(import.meta.env.VITE_DISCORD_CLIENT_ID) && (params.has("frame_id") || params.has("instance_id"));
}

async function initializeDiscord(): Promise<PlatformSession> {
  const clientId = import.meta.env.VITE_DISCORD_CLIENT_ID;
  if (!clientId) throw new Error("VITE_DISCORD_CLIENT_ID is missing.");
  const { DiscordSDK } = await import("@discord/embedded-app-sdk");
  const sdk: DiscordSDKType = new DiscordSDK(clientId);
  await sdk.ready();

  const { code } = await sdk.commands.authorize({
    client_id: clientId,
    response_type: "code",
    state: "",
    prompt: "none",
    scope: ["identify", "applications.commands"],
  });
  const response = await fetch("/.proxy/api/token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code, instanceId: sdk.instanceId }),
  });
  if (!response.ok) throw new Error("Discord sign-in could not be completed.");
  const token = (await response.json()) as { access_token: string; session: string };
  const auth = await sdk.commands.authenticate({ access_token: token.access_token });
  if (!auth?.user) throw new Error("Discord did not return a user.");

  let participants: number | undefined;
  try {
    participants = (await sdk.commands.getInstanceConnectedParticipants()).participants.length;
  } catch {
    // Participant presence is helpful context, but is not required to play.
  }

  return {
    mode: "discord",
    identity: {
      id: auth.user.id,
      name: auth.user.global_name ?? auth.user.username,
    },
    roomId: sdk.instanceId,
    session: token.session,
    apiPrefix: "/.proxy",
    participants,
  };
}

function initializeBrowser(): PlatformSession {
  const params = new URLSearchParams(window.location.search);
  const storedID = localStorage.getItem("cambio:user-id") || `web-${randomID()}`;
  localStorage.setItem("cambio:user-id", storedID);
  const identity = {
    id: clean(params.get("user"), storedID),
    name: clean(params.get("name"), localStorage.getItem("cambio:name") || "Guest Player"),
  };
  localStorage.setItem("cambio:name", identity.name);
  return {
    mode: "browser",
    identity,
    roomId: clean(params.get("room"), "table-demo"),
    apiPrefix: "",
  };
}

export async function initializePlatform(): Promise<PlatformSession> {
  return isDiscordFrame() ? initializeDiscord() : initializeBrowser();
}

export function websocketURL(session: PlatformSession): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  const url = new URL(`${protocol}//${window.location.host}${session.apiPrefix}/ws`);
  url.searchParams.set("room", session.roomId);
  if (session.session) {
    url.searchParams.set("session", session.session);
  } else {
    url.searchParams.set("user", session.identity.id);
    url.searchParams.set("name", session.identity.name);
  }
  return url.toString();
}

