#!/bin/bash

# Container startup script
# Downloads models if needed, then starts nginx

set -e

echo "Starting sub-proxy container..."

# Download models if they don't exist
/app/download_models.sh

echo "Starting nginx..."
nginx -g "daemon off;"
