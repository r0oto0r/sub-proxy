#!/bin/bash

# Audio streamer wrapper script for proper logging
STREAM_NAME="$1"
LOG_FILE="/var/log/audio-streamer-${STREAM_NAME}.log"

# Ensure log directory exists
mkdir -p /var/log

# Log start time and stream name to both stdout and log file
echo "$(date): [WRAPPER] Starting audio streamer for stream: $STREAM_NAME" | tee "$LOG_FILE"
echo "$(date): [WRAPPER] Log file: $LOG_FILE" | tee -a "$LOG_FILE"
echo "$(date): [WRAPPER] Current working directory: $(pwd)" | tee -a "$LOG_FILE"
echo "$(date): [WRAPPER] Audio streamer binary exists: $(ls -la /app/audio-streamer)" | tee -a "$LOG_FILE"

# Test if the binary is executable
if [ ! -x "/app/audio-streamer" ]; then
    echo "$(date): [WRAPPER] ERROR: /app/audio-streamer is not executable!" | tee -a "$LOG_FILE"
    exit 1
fi

# Start the Go application and capture all output
echo "$(date): [WRAPPER] Executing: /app/audio-streamer $STREAM_NAME" | tee -a "$LOG_FILE"

# Use a simpler approach - just redirect everything to the log file and use tee to also show on stdout
exec /app/audio-streamer "$STREAM_NAME" 2>&1 | tee -a "$LOG_FILE"
