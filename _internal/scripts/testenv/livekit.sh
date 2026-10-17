#!/bin/zsh
# 测试环境 LiveKit 启停（仅 127.0.0.1）。用法: livekit.sh start|stop|status
set -o pipefail
ROOT="$HOME/cs-testenv"
source "$ROOT/secrets/test.env"
log() { print -r -- "$(date '+%Y-%m-%dT%H:%M:%S+08:00') $*" | tee -a "$ROOT/logs/testenv.log"; }
case "$1" in
  start)
    if curl -s --max-time 2 http://127.0.0.1:17880/ | grep -q OK; then log "livekit already running"; exit 0; fi
    sed "s/__API_KEY__/$LIVEKIT_API_KEY/" "$ROOT/livekit.yaml" > "$ROOT/run/livekit.yaml" && chmod 600 "$ROOT/run/livekit.yaml"
    LIVEKIT_KEYS="$LIVEKIT_API_KEY: $LIVEKIT_API_SECRET" nohup "$ROOT/bin/livekit-server" --config "$ROOT/run/livekit.yaml" >> "$ROOT/logs/livekit.log" 2>&1 &
    print -r -- $! > "$ROOT/run/livekit.pid"
    for i in {1..20}; do curl -s --max-time 2 http://127.0.0.1:17880/ | grep -q OK && { log "livekit up on 127.0.0.1:17880 (tcp 17881 / udp 17882)"; exit 0; }; sleep 1; done
    log "livekit FAILED to start"; tail -20 "$ROOT/logs/livekit.log"; exit 1 ;;
  stop) [[ -f "$ROOT/run/livekit.pid" ]] && kill "$(cat "$ROOT/run/livekit.pid")" 2>/dev/null && log "livekit stopped"; exit 0 ;;
  status) curl -s --max-time 2 http://127.0.0.1:17880/; echo ;;
  *) echo "usage: $0 start|stop|status"; exit 2 ;;
esac
