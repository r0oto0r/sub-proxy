#!/bin/bash

set -e

echo "Starting sub-proxy container..."

huggingface-cli login --token $HUGGINGFACE_TOKEN

echo "Starting WhisperLiveKit server in background..."
whisperlivekit-server \
	--model NbAiLab/whisper-large-v2-nob \
	--host 0.0.0.0 \
	--port 8000 \
	--language de \
	--task translate \
	--backend whisper_timestamped \
	--vac \
	-l DEBUG \
	--buffer_trimming segment 2>&1 | \
	while IFS= read -r line; do
		echo -e "\033[36m[WhisperLiveKit]\033[0m $line"
	done &

echo "Starting Go HTTP server in background..."
/app/sub-proxy 2>&1 | \
	while IFS= read -r line; do
		echo -e "\033[32m[SubProxy]\033[0m $line"
	done &

echo "Starting nginx..."
nginx -g "daemon off;" 2>&1 | \
	while IFS= read -r line; do
		echo -e "\033[33m[Nginx]\033[0m $line"
	done