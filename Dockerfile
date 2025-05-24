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
	wget \
	curl \
	build-essential \
	cmake \
	meson \
	ninja-build \
	pkg-config \
	libssl-dev \
	zlib1g-dev \
	libpcre3-dev \
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
	libgtk-3-dev \
	libcairo2-dev \
	libpango1.0-dev \
	libgdk-pixbuf2.0-dev \
	libatk1.0-dev \
	libsoup2.4-dev \
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

# Build and install nginx with RTMP module
WORKDIR /tmp

# Build transcriberbin module
RUN git clone https://github.com/GStreamer/gst-plugins-rs.git /tmp/gst-plugins-rs

# Build specific plugins we need (transcriberbin and related)
WORKDIR /tmp/gst-plugins-rs
RUN cargo cbuild -p gst-plugin-closedcaption --release && \
	cargo cinstall -p gst-plugin-closedcaption --release --prefix=/usr

# Set GST_PLUGIN_PATH to include our new plugins
ENV GST_PLUGIN_PATH="/usr/lib/gstreamer-1.0"


# Download nginx and nginx-rtmp-module
RUN wget http://nginx.org/download/nginx-1.26.0.tar.gz && \
	tar -xzf nginx-1.26.0.tar.gz && \
	git clone https://github.com/arut/nginx-rtmp-module.git

# Configure and build nginx with RTMP module
WORKDIR /tmp/nginx-1.26.0
RUN ./configure \
	--prefix=/etc/nginx \
	--sbin-path=/usr/sbin/nginx \
	--modules-path=/usr/lib/nginx/modules \
	--conf-path=/etc/nginx/nginx.conf \
	--error-log-path=/var/log/nginx/error.log \
	--http-log-path=/var/log/nginx/access.log \
	--pid-path=/var/run/nginx.pid \
	--lock-path=/var/run/nginx.lock \
	--http-client-body-temp-path=/var/cache/nginx/client_temp \
	--http-proxy-temp-path=/var/cache/nginx/proxy_temp \
	--http-fastcgi-temp-path=/var/cache/nginx/fastcgi_temp \
	--http-uwsgi-temp-path=/var/cache/nginx/uwsgi_temp \
	--http-scgi-temp-path=/var/cache/nginx/scgi_temp \
	--with-debug \
	--with-compat \
	--with-file-aio \
	--with-threads \
	--with-http_addition_module \
	--with-http_auth_request_module \
	--with-http_dav_module \
	--with-http_flv_module \
	--with-http_gunzip_module \
	--with-http_gzip_static_module \
	--with-http_mp4_module \
	--with-http_random_index_module \
	--with-http_realip_module \
	--with-http_secure_link_module \
	--with-http_slice_module \
	--with-http_ssl_module \
	--with-http_stub_status_module \
	--with-http_sub_module \
	--with-http_v2_module \
	--with-stream \
	--with-stream_realip_module \
	--with-stream_ssl_module \
	--with-stream_ssl_preread_module \
	--add-module=/tmp/nginx-rtmp-module && \
	make && \
	make install

# Create directories with root ownership
RUN mkdir -p /var/cache/nginx/client_temp && \
	mkdir -p /var/cache/nginx/proxy_temp && \
	mkdir -p /var/cache/nginx/fastcgi_temp && \
	mkdir -p /var/cache/nginx/uwsgi_temp && \
	mkdir -p /var/cache/nginx/scgi_temp

# Copy nginx configuration and HTML files
COPY nginx.conf /etc/nginx/nginx.conf
RUN mkdir -p /usr/share/nginx/html
COPY index.html /usr/share/nginx/html/index.html

# Clean up build dependencies
WORKDIR /
RUN rm -rf /tmp/nginx-1.26.0* /tmp/nginx-rtmp-module /tmp/gst-plugins-rs

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
