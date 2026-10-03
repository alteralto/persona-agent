#!/usr/bin/env bash
# Nightly self-improvement: mine new lines from the decision journal, weak-label them, retune
# Laya's decision setup, and keep the result only if it wins on the hand-labelled test half.
cd "$(dirname "$0")/.." || exit 1
log="logs/nightly-$(date +%F).txt"
{
  echo "== $(date '+%F %T') benchmine"
  ./bin/benchmine.exe
  echo "== $(date '+%F %T') layatune"
  ./bin/layatune.exe -rounds 3 | grep -v '^   want'
  echo "== done; restart the agent to pick up data/laya_criteria.json if it changed"
} >> "$log" 2>&1
echo "report: $log"
