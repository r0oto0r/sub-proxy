#!/bin/bash

# Download Whisper models script
# This script checks if models exist and downloads them if they don't

set -e

MODEL_DIR="/app/models"
WHISPER_LARGE_V3_DIR="${MODEL_DIR}/whisper-large-v3"
WHISPER_LARGE_V3_TURBO_DIR="${MODEL_DIR}/whisper-large-v3-turbo"

echo "Checking for Whisper models in ${MODEL_DIR}..."

# Create models directory if it doesn't exist
mkdir -p "${MODEL_DIR}"

# Check if whisper-large-v3 exists and has content
if [ ! -d "${WHISPER_LARGE_V3_DIR}" ] || [ -z "$(ls -A "${WHISPER_LARGE_V3_DIR}" 2>/dev/null)" ]; then
    echo "Downloading whisper-large-v3 model..."
    cd "${MODEL_DIR}"
    git lfs install
    git clone https://huggingface.co/openai/whisper-large-v3
else
    echo "whisper-large-v3 model already exists"
fi

# Check if whisper-large-v3-turbo exists and has content
if [ ! -d "${WHISPER_LARGE_V3_TURBO_DIR}" ] || [ -z "$(ls -A "${WHISPER_LARGE_V3_TURBO_DIR}" 2>/dev/null)" ]; then
    echo "Downloading whisper-large-v3-turbo model..."
    cd "${MODEL_DIR}"
    git lfs install
    git clone https://huggingface.co/openai/whisper-large-v3-turbo
else
    echo "whisper-large-v3-turbo model already exists"
fi

echo "Model download check completed"
