# persona-agent

**Pyhtun** — a home-loving hobbit who reluctantly supplies a flying wizard with fireworks — is an AI
character that plays Minecraft with real players. He hears the game's voice chat, decides what to
do in about a tenth of a second, answers out loud in his own voice, explores, remembers places and
people, and talks with other AI characters on the server.

The design goal is **speed**: from the end of a player's phrase to the first sound of the answer
in about two seconds, most of it speech recognition.

## How it works

```
 game voice chat ──Py4J──▶ voice/service.py ──(per player)──▶ Whisper large-v3-turbo ──▶ ┐
 game text chat ──MCP getRecentChat──────────────────────────────────────────────────────▶ ┤
                                                                                           ▼
                                       Go agent (cmd/agent)
   ┌───────────── decide ─────────────┐   ┌──── act ─────┐   ┌──────── speak ────────┐
   │ keyword rules (explicit verbs)   │   │ UnionClef    │   │ GigaChat-2 (fast)     │
   │ Laya: ru + en questions, ~100 ms │──▶│ via MCP:     │──▶│ GigaChat-2-Max for    │
   │ agreement + threshold            │   │ @follow,     │   │ "where/how" & actions │
   └──────────────────────────────────┘   │ @goto, flyTo │   │ critic: no false      │
                                          └──────────────┘   │ "I'm coming" promises │
                                                             └───────────┬───────────┘
                                                text chat ◀──────────────┤
                                     Piper (per sentence) ──▶ VB-Cable ──▶ game voice chat
```

- **Laya chooses, the LLM talks.** Laya (a fast non-autoregressive decision model) picks one of a
  few actions — wait, follow, stop, defend, bring rockets, lead home. Explicit verbs ("подойди",
  "замри", "крипер", "полетели") are caught by rules first; Laya handles the rest. The LLM is
  only asked for the words.
- **One GigaChat stream.** Replies go through a single worker; a newer reason to speak replaces
  a waiting one instead of queueing behind it.
- **Voice is per player.** UnionClef forwards every Simple Voice Chat packet tagged with the
  speaker's name, so speakers are separated without diarisation.
- **Shared-server manners.** The pathfinder mines only natural terrain and places nothing (fork
  tool `setBuildPolicy`; with the upstream mod, a no-break zone around home).

## What the character does

| Skill | How |
|---|---|
| answer by voice and chat | Whisper → rules + Laya → GigaChat → Piper, ~1.9 s end to end |
| follow / stop / lead home | UnionClef `@follow`, `@stop`, `@goto` |
| stay with companions, start conversations | idle turns: invite someone to a remembered place, or talk on a rotating topic |
| explore | chunk by chunk around home; surface map + sign text → `data/places.json` |
| remember people | `data/people/<nick>.md`: dated exchanges + short notes (summarised while quiet) |
| fly on an elytra | fork tool `flyTo`: equip, take off, climb on rockets, glide down near the target |
| creative and survival | game mode from `getGameState`; creative takes items from the creative tab |

## Self-improvement loop

Every decision goes to `data/decisions.jsonl` (with a *repeat* flag when the same player asks
again within 30 s — a sign the decision missed). Overnight (`scripts/nightly.sh`):

1. `cmd/benchmine` takes new lines from the journal, weak-labels them with GigaChat-2-Max into
   `bench/laya_cases.auto.json`.
2. `cmd/layatune` searches the decision setup (rule, threshold) and asks the LLM to rewrite the
   option descriptions. The bench is split in half: weak labels only ever join the **train**
   half; the **test** half is hand-labelled.
3. The result is written to `data/laya_criteria.json` — which the agent loads at start — **only if
   it beats the current setup on the test half.** LLM rewrites that made things worse were
   rejected by this gate on the first runs.

First runs on 63 lines from a live session: Laya alone 66 % → with keyword rules 81 % → with a
tuned threshold 84 % on the test half (the rules were written having seen the whole bench, so
this is optimistic until new lines come in).

## Layout

| Path | What |
|---|---|
| `cmd/agent` | the agent |
| `cmd/probe`, `cmd/layatune`, `cmd/benchmine`, `cmd/llmbench` | decision probe, tuning, bench mining, LLM model comparison |
| `internal/agent` | decision loop, rules, critic, exploration, outings, fork tools |
| `internal/{laya,giga,mcp,voice,chatlog,explore,memory}` | clients and building blocks |
| `voice/service.py` | Py4J voice feed, Whisper STT, Piper TTS to the virtual cable |
| `bench/` | hand-labelled decision bench |
| `scripts/run-agent.sh`, `scripts/nightly.sh` | start the agent; nightly tuning |

Runtime data (`data/`, `logs/`, models, `.env`) is not committed.

## Running

Needs: Minecraft 1.21.11 (Fabric) with UnionClef (ideally the fork
[alteralto/unionclef](https://github.com/alteralto/unionclef), branch `pyhtun`), Simple Voice Chat
with the microphone set to `CABLE Output` and voice activation, `laya-serve` on `127.0.0.1:8090`,
GigaChat credentials in `.env` (see `.env.example`).

```bash
go build -o bin/pyhtun.exe ./cmd/agent
.venv-voice/Scripts/python.exe voice/service.py   # voice: STT + TTS + Py4J
scripts/run-agent.sh
```

The orchestrator is in Go for now; a Rust port is planned.
