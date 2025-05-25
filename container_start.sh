#!/bin/bash

set -e

echo "Starting sub-proxy container..."

echo "Starting WhisperLiveKit server in background..."
python3 -m whisperlivekit.server --model large-v3 --host 0.0.0.0 --port 8000 &

# Wait a moment for WhisperLiveKit to start
sleep 5

echo "Starting nginx..."
nginx -g "daemon off;"
