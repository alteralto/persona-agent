#!/usr/bin/env bash
# Start the agent for play with NetTyan and Pon4ikYT on agicraft. Secrets come from .env.
cd "$(dirname "$0")/.." || exit 1
export INTRO=false \
  OTHER_BOTS=NetTyan,Pon4ikYT \
  COMPANIONS=NetTyan \
  BOT_ALIASES='пыхтун,арк' \
  OTHERS_NAMES='пончик,пон,pon4ik,pon4ikyt,nettyan,нетян,неттян' \
  EXPLORE_AFTER=20s \
  IDLE_EVERY=60s \
  GIGACHAT_MODEL=GigaChat-2 \
  GIGACHAT_SMART_MODEL=GigaChat-2-Max
exec ./bin/pyhtun.exe >> logs/pyhtun.log 2>&1
