#!/bin/bash

# Start script for sub-proxy Docker container
# This script builds and runs the Docker container with GPU support

set -e  # Exit on any error

# Configuration
CONTAINER_NAME="sub-proxy"
IMAGE_NAME="sub-proxy"
HTTP_PORT="8080"
RTMP_PORT="1935"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}Starting sub-proxy Docker container...${NC}"

# Check if Docker is running
if ! docker info &> /dev/null; then
    echo -e "${RED}Error: Docker is not running. Please start Docker first.${NC}"
    exit 1
fi

# Check for NVIDIA Docker runtime (for GPU support)
if ! docker info | grep -q "nvidia"; then
    echo -e "${YELLOW}Warning: NVIDIA Docker runtime not detected. GPU features may not work.${NC}"
fi

# Stop and remove existing container if it exists
if docker ps -a --format 'table {{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo -e "${YELLOW}Stopping and removing existing container...${NC}"
    docker stop ${CONTAINER_NAME} &> /dev/null || true
    docker rm ${CONTAINER_NAME} &> /dev/null || true
fi

# Build the Docker image
echo -e "${BLUE}Building Docker image...${NC}"
docker build -t ${IMAGE_NAME} .

if [ $? -eq 0 ]; then
    echo -e "${GREEN}Docker image built successfully!${NC}"
else
    echo -e "${RED}Failed to build Docker image.${NC}"
    exit 1
fi

# Run the container with GPU support
echo -e "${BLUE}Starting container...${NC}"
docker run -d \
    --name ${CONTAINER_NAME} \
    --gpus all \
    -p ${HTTP_PORT}:80 \
    -p ${RTMP_PORT}:1935 \
    --restart unless-stopped \
    ${IMAGE_NAME}

if [ $? -eq 0 ]; then
    echo -e "${GREEN}Container started successfully!${NC}"
    echo -e "${GREEN}HTTP Server: http://localhost:${HTTP_PORT}${NC}"
    echo -e "${GREEN}RTMP Server: rtmp://localhost:${RTMP_PORT}/live${NC}"
    echo ""
    echo -e "${BLUE}Container logs:${NC}"
    docker logs -f ${CONTAINER_NAME}
else
    echo -e "${RED}Failed to start container.${NC}"
    exit 1
fi
