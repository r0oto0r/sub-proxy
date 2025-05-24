# Use CUDA base image with Ubuntu
FROM nvidia/cuda:12.9.0-base-ubuntu24.04

# Set environment variables
ENV DEBIAN_FRONTEND=noninteractive
ENV PYTHONUNBUFFERED=1
ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility

# Install system dependencies
RUN apt-get update && apt-get install -y \
	python3 \
	python3-pip \
	python3-dev \
	python3-venv \
	pipx \
	git \
	curl \
	build-essential \
	cmake \
	meson \
	ninja-build \
	pkg-config \
	libssl-dev \
	zlib1g-dev \
	ffmpeg \
	gstreamer1.0-tools \
	gstreamer1.0-plugins-base \
	gstreamer1.0-plugins-good \
	gstreamer1.0-plugins-bad \
	gstreamer1.0-plugins-ugly \
	gstreamer1.0-libav \
	libgstreamer1.0-dev \
	libgstreamer-plugins-base1.0-dev \
	libgstreamer-plugins-bad1.0-dev \
	gstreamer1.0-plugins-bad-apps \
	libglib2.0-dev \
	libcairo2-dev \
	libpango1.0-dev \
	libclang-dev \
	clang \
	&& rm -rf /var/lib/apt/lists/*

# Install Rust
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
ENV PATH="/root/.cargo/bin:${PATH}"

# Install cargo-c for building C libraries from Rust
RUN cargo install cargo-c

# Install Python dependencies
RUN pipx install torch torchaudio --include-deps --index-url https://download.pytorch.org/whl/cu121

# Install OpenAI Whisper
RUN pipx install openai-whisper

# Install git-lfs for downloading large model files
RUN apt-get update && apt-get install -y git-lfs && rm -rf /var/lib/apt/lists/*

# Set environment variable for Whisper model cache
ENV WHISPER_CACHE_DIR=/app/models

# Set environment variable for gst-whisper model path
ENV WHISPER_MODEL_PATH=/app/models/ggml-large-v3-turbo.bin

# Build and install nginx with RTMP module
WORKDIR /tmp

# Build and install gst-whisper plugin
RUN git clone https://github.com/avstack/gst-whisper.git /tmp/gst-whisper

WORKDIR /tmp/gst-whisper
RUN cargo cbuild --release && \
	cargo cinstall --release --prefix=/usr

# Set GST_PLUGIN_PATH to include our new plugins
ENV GST_PLUGIN_PATH="/usr/lib/gstreamer-1.0"

# Install nginx with RTMP module from packages
RUN apt-get update && apt-get install -y \
	nginx \
	libnginx-mod-rtmp \
	&& rm -rf /var/lib/apt/lists/*

# Copy nginx configuration and HTML files
COPY nginx.conf /etc/nginx/nginx.conf
RUN mkdir -p /usr/share/nginx/html
COPY index.html /usr/share/nginx/html/index.html

# Clean up build dependencies
WORKDIR /
RUN rm -rf /tmp/gst-whisper

# Create a working directory for the application
WORKDIR /app

# Copy and make stream processing script executable
COPY process_stream.sh /app/process_stream.sh
RUN chmod +x /app/process_stream.sh

# Copy model download script
COPY download_models.sh /app/download_models.sh
RUN chmod +x /app/download_models.sh

# Copy container startup script
COPY container_start.sh /app/container_start.sh
RUN chmod +x /app/container_start.sh

# Expose ports
EXPOSE 80 1935

# Default command - run startup script
CMD ["/app/container_start.sh"]
