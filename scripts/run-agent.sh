#!/usr/bin/env bash
# Start the agent for play with NetTyan and Pon4ikYT on agicraft. Secrets come from .env.
cd "$(dirname "$0")/.." || exit 1
# No env values starting with "/": Git Bash rewrites them into Windows paths ("/gamemode creative"
# became "C:/Program Files/Git/gamemode creative"). The creative command is the agent's default.
# Flight testing: FLIGHT_SHARE=0.8 ACTIVITY_EVERY=30s scripts/run-agent.sh
export INTRO=false \
  OTHER_BOTS=NetTyan,Pon4ikYT \
  COMPANIONS=NetTyan \
  BOT_ALIASES='пыхтун,арк' \
  OTHERS_NAMES='пончик,пон,pon4ik,pon4ikyt,nettyan,нетян,неттян' \
  AUTO_CREATIVE=true \
  EXPLORE_AFTER=20s \
  IDLE_EVERY=60s \
  ACTIVITY_EVERY="${ACTIVITY_EVERY:-30s}" \
  FLIGHT_SHARE="${FLIGHT_SHARE:-0}" \
  GIGACHAT_MODEL=GigaChat-2 \
  GIGACHAT_SMART_MODEL=GigaChat-2-Max
exec ./bin/pyhtun.exe >> logs/pyhtun.log 2>&1
