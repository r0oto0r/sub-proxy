#!/bin/bash

set -e

echo "Starting sub-proxy container..."

echo "$HUGGINGFACE_TOKEN" | huggingface-cli login --token

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
