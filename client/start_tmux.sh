#!/usr/bin/env bash
#
# start_tmux.sh — create a detached 'boot' tmux session,
# cd into robot-webrtc, git pull, then start servo and client.
#

SESSION="boot"
PROJ_ROOT="$HOME/robot-webrtc"
SERVER_IP="146.190.175.159"
REMOTE_USER="streaming"

tmux has-session -t "$SESSION" 2>/dev/null && exit 0

# 1. Create session and windows
tmux new-session -d -s "$SESSION" -n servo -c "$PROJ_ROOT"

# --- TUNNEL WINDOW ---
tmux new-window -t "$SESSION" -n tunnel -c "$PROJ_ROOT"

# This script waits for network then opens the tunnel (adjust as needed)
tmux send-keys -t "$SESSION:tunnel" "
until ping -c1 $SERVER_IP >/dev/null 2>&1; do
  echo 'Waiting for network...'
  sleep 2
done
echo 'Network available. Opening reverse tunnel...'
while true; do
  ssh -N -R 2222:localhost:22 $REMOTE_USER@$SERVER_IP || true
  echo 'Tunnel disconnected. Retrying in 5s...'
  sleep 5
done
" C-m

# --- SERVO WINDOW ---
tmux send-keys -t "$SESSION:servo" "git pull" C-m
tmux send-keys -t "$SESSION:servo" "pkill ffmpeg || true" C-m
tmux send-keys -t "$SESSION:servo" "go run ./cmd/servo/" C-m

# --- CLIENT WINDOW ---
tmux new-window -t "$SESSION" -n client -c "$PROJ_ROOT"
tmux send-keys -t "$SESSION:client" "go run ./cmd/client/" C-m

echo "🚀 tmux session '$SESSION' created with windows: tunnel, servo, client."
