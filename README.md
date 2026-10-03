# persona-agent

AI character that plays Minecraft with real players: it hears voice chat, decides what to do,
and answers with its own voice and personality.

## Idea

- **Laya** (fast non-autoregressive decision engine) picks the bot's next *action* in tens of
  milliseconds: follow, flee, fight, mine, wait, reply or stay silent.
- A **cloud LLM** writes the character's lines, and is called only when Laya decides to reply.
- **UnionClef** (Fabric mod) executes actions in the game via its MCP server; game events and
  per-player voice come through its Py4J callbacks.
- Lightweight **STT / TTS** tuned for a laptop GPU (RTX 3060 Laptop, 6 GB).

## Layout

- `cmd/agent` — orchestrator (Go for now, Rust port later).

## Status

Early design. Nothing runs yet.
