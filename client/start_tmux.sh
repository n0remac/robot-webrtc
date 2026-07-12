#!/usr/bin/env bash
# Start the complete robot stack in one tmux pane.

SESSION="boot"
PROJ_ROOT="$HOME/robot-webrtc"

tmux has-session -t "$SESSION" 2>/dev/null && exit 0

tmux new-session -d -s "$SESSION" -n robot -c "$PROJ_ROOT"
tmux send-keys -t "$SESSION:robot" "git pull && exec go run ./cmd/client -site-url \"\${ROBOT_SITE_URL:-http://165.227.55.254:8081}\"" C-m

echo "Robot started in tmux session '$SESSION' (window: robot)."
