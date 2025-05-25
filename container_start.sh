#!/bin/bash

set -e

echo "Starting sub-proxy container..."

huggingface-cli login --token $HUGGINGFACE_TOKEN

echo "Starting WhisperLiveKit server in background..."
whisperlivekit-server \
	--model large-v3 \
	--host 0.0.0.0 \
	--port 8000 \
	--language de \
	--task translate \
	--vac \
	--buffer_trimming sentence &

echo "Starting nginx..."
nginx -g "daemon off;"
