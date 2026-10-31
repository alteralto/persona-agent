# Voice from the game: UDP, not Py4J

The voice service hears players through the game: UnionClef gets every Simple Voice Chat packet
already tagged with the speaker's name and passes it on. Until November 2026 it passed it over Py4J.
Now it sends one UDP datagram per packet to `127.0.0.1:25360`. This note says why.

## What went wrong with Py4J

Py4J is a two-way bridge: the mod runs a gateway server (`25333`) and the Python side runs a
callback server (`25334`) that the mod calls `onVoiceFeed` on, about 50 times a second for each
speaking player. That gave us three kinds of trouble.

- **The gateway restarted on every settings change.** Each `@set` reloads the settings, and the
  reload restarted the gateway. With the voice service connected, the new gateway often did not
  bind at all: port 25333 stayed closed and the bot was deaf until someone ran another `@set` by
  hand while nothing was connected. A local test reproduces it: 25333 listening before `@set`,
  closed after.
- **Two sockets that must find each other.** The service had to find the right game (the port moves
  up by two when a second client on the machine holds it), hand over its callback, notice when the
  game went away and do it again. About sixty lines of reconnect logic, and the bot still sat deaf
  for an evening after the mod once saved a fallback port to the settings that both clients share.
- **A round trip per packet.** A synchronous call per 20 ms packet outran the consumer and grew the
  mod's heap until the JVM died (the mod now drops stale packets in a bounded queue). Voice needs a
  steady stream, not delivery of every frame, and certainly not an answer.

## What it is now

The mod sends a datagram per voice packet and does not wait for anything:

```
"PYV1" | u8 n | own nick (n bytes, UTF-8) | u8 m | speaker (m bytes) | PCM 16-bit LE, 48 kHz mono
```

- **Nothing to connect.** The service binds the port and reads. The game can start before or after
  it, restart or reload its settings: the next packet just arrives. Nobody listening costs the mod
  nothing.
- **Several bots on one machine.** Every client sends to the same port with its own player's name
  first; each voice service keeps the packets of its `BOT_NICK`.
- **Loopback only.** The mod sends to `127.0.0.1`, the service binds `127.0.0.1`. Losing a packet on
  loopback is rare, and losing one costs 20 ms of a phrase.

The port is the mod setting `voiceUdpPort` (`altoclef_settings.json`, default 25360, 0 turns it
off) and the service flag `--game-port`. The Py4J voice callback is still in the mod for other
Python clients; the voice service no longer uses it.

The mod side also stopped restarting the gateway on a settings reload unless the port changed
(UnionClef fork, branch `pyhtun`), so other Py4J clients keep working across `@set` too.

## What still uses Py4J

Nothing in the agent. Our local test bench uses it to take game-only frames for test videos
(`getScreenshot`), and the agent itself talks to the mod over MCP (HTTP on 25350).
