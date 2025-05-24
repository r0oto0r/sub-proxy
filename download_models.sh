#!/bin/bash

# Download Whisper models script
# This script checks if models exist and downloads them if they don't

set -e

MODEL_DIR="/app/models"
GGML_LARGE_V3_TURBO_FILE="${MODEL_DIR}/ggml-large-v3-turbo.bin"

echo "Checking for Whisper model in ${MODEL_DIR}..."

# Create models directory if it doesn't exist
mkdir -p "${MODEL_DIR}"

# Check if ggml-large-v3-turbo.bin exists
if [ ! -f "${GGML_LARGE_V3_TURBO_FILE}" ]; then
    echo "Downloading ggml-large-v3-turbo.bin model..."
    cd "${MODEL_DIR}"
	curl -L -o ggml-large-v3-turbo.bin --progress-bar https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin
else
	echo "ggml-large-v3-turbo.bin model already exists"
fi

echo "Model download check completed"
