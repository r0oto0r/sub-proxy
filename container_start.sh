#!/bin/bash

set -e

echo "Starting sub-proxy container..."

/app/download_models.sh

echo "Starting nginx..."
nginx -g "daemon off;"
