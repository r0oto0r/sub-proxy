#!/bin/bash

set -e

echo "Starting sub-proxy container..."

python3 -c "from huggingface_hub.commands.user import _login; _login(token='$HUGGINGFACE_TOKEN')"

echo "Starting WhisperLiveKit server in background..."
whisperlivekit-server \
	--model large-v3 \
	--host 0.0.0.0 \
	--port 8000 \
	--language de \
	--task translate \
	--diarization \
	--buffer_trimming sentence &

echo "Starting nginx..."
nginx -g "daemon off;"
