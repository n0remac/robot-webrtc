#!/usr/bin/env bash
#
# start_tmux.sh — create a detached 'boot' tmux session,
# cd into robot-webrtc, git pull, then start servo and client.
#

# session name
SESSION="boot"
# project root directory
PROJ_ROOT="$HOME/robot-webrtc"

# If the session already exists, exit
tmux has-session -t "$SESSION" 2>/dev/null && exit 0

# Create a new detached session, first window for servo server
tmux new-session -d \
  -s "$SESSION" \
  -n servo \
  -c "$PROJ_ROOT"

# In the 'servo' window:
# 1) open reverse SSH tunnel
# 2) pull latest code
# 3) kill any lingering ffmpeg processes
# 4) run the servo gRPC server
tmux send-keys -t "$SESSION:servo" "ssh -Nf -R 2222:localhost:22 streaming@146.190.175.159" C-m
tmux send-keys -t "$SESSION:servo" "git pull" C-m
tmux send-keys -t "$SESSION:servo" "pkill ffmpeg || true" C-m
tmux send-keys -t "$SESSION:servo" "go run ./cmd/servo/" C-m

# Create a second window for the client
tmux new-window -t "$SESSION" \
  -n client \
  -c "$PROJ_ROOT"

# In the 'client' window:
# run the WebRTC client
tmux send-keys -t "$SESSION:client" "go run ./cmd/client/" C-m

echo "🚀 tmux session '$SESSION' created with windows: servo, client."
