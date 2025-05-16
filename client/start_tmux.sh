#!/usr/bin/env bash
#
# start_tmux.sh — create a detached 'boot' tmux session,
# cd into robot-webrtc, git pull, then go run .
#

# session name
SESSION="boot"
# project directory
PROJ_DIR="$HOME/robot-webrtc/client"

# ensure TMUX socket dir is correct
export HOME="$HOME"

# If the session already exists, do nothing
tmux has-session -t "$SESSION" 2>/dev/null && exit 0

# create a new detached session, with one window named 'robot-webrtc'
tmux new-session -d \
  -s "$SESSION" \
  -n robot-webrtc \
  -c "$PROJ_DIR"

# run your commands in that window
tmux send-keys -t "${SESSION}:robot-webrtc" "ssh -Nf -R 2222:localhost:22 streaming@146.190.175.159" C-m
tmux send-keys -t "${SESSION}:robot-webrtc" "git pull" C-m
tmux send-keys -t "${SESSION}:robot-webrtc" "pkill ffmpeg" C-m
tmux send-keys -t "${SESSION}:robot-webrtc" "go run ./client.go" C-m