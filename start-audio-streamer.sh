#!/bin/bash

# Audio streamer wrapper script for proper logging
STREAM_NAME="$1"
LOG_FILE="/var/log/audio-streamer-${STREAM_NAME}.log"
PIPE_FILE="/var/log/audio-streamer-${STREAM_NAME}.pipe"

# Ensure log directory exists
mkdir -p /var/log

# Create named pipe for real-time log forwarding
if [ ! -p "$PIPE_FILE" ]; then
    mkfifo "$PIPE_FILE"
fi

# Log start time and stream name
echo "$(date): Starting audio streamer for stream: $STREAM_NAME" | tee -a "$LOG_FILE"

# Start a background process to read from the pipe and forward to both log file and stdout
(
    while true; do
        if [ -p "$PIPE_FILE" ]; then
            cat "$PIPE_FILE" | while IFS= read -r line; do
                timestamp=$(date '+%Y-%m-%d %H:%M:%S')
                log_line="$timestamp [audio-streamer-$STREAM_NAME] $line"
                echo "$log_line" | tee -a "$LOG_FILE"
            done
        fi
        sleep 1
    done
) &

# Start the Go application and redirect output to the pipe
# Use exec to replace the shell process with the Go application
/app/audio-streamer "$STREAM_NAME" > "$PIPE_FILE" 2>&1
