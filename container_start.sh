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
	--buffer_trimming sentence 2>&1 | \
	while IFS= read -r line; do
		echo -e "\033[36m[WhisperLiveKit]\033[0m $line"
	done &

# Start log monitoring for audio streamer logs in background
echo "Starting log monitor for audio streamer..."
mkdir -p /var/log
(
	# Use inotify to monitor for new log files and tail them
	while true; do
		# Check for existing log files
		for logfile in /var/log/audio-streamer-*.log; do
			if [ -f "$logfile" ] && [ ! -f "$logfile.monitored" ]; then
				echo "Found new audio streamer log file: $logfile"
				touch "$logfile.monitored"
				tail -f "$logfile" | while IFS= read -r line; do
					echo -e "\033[32m[AudioStreamer]\033[0m $line"
				done &
			fi
		done
		
		# Check for new pipe files
		for pipefile in /var/log/audio-streamer-*.pipe; do
			if [ -p "$pipefile" ] && [ ! -f "$pipefile.monitored" ]; then
				echo "Found new audio streamer pipe: $pipefile"
				touch "$pipefile.monitored"
				cat "$pipefile" | while IFS= read -r line; do
					echo -e "\033[32m[AudioStreamer]\033[0m $line"
				done &
			fi
		done
		
		sleep 2
	done
) &

echo "Starting nginx..."
nginx -g "daemon off;"
