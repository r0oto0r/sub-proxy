# Use CUDA 12.8 image (matching WhisperLiveKit official)
FROM nvidia/cuda:12.8.1-cudnn-runtime-ubuntu22.04

# Set environment variables
ENV NGINX_VERSION=1.26.0
ENV DEBIAN_FRONTEND=noninteractive
ENV PATH="/usr/local/go/bin:${PATH}"
ENV PYTHONUNBUFFERED=1

# Install system dependencies
RUN apt-get update && apt-get install -y \
	git \
	curl \
	wget \
	build-essential \
	cmake \
	pkg-config \
	libssl-dev \
	zlib1g-dev \
	libpcre3-dev \
	libxml2-dev \
	libxslt1-dev \
	libgd-dev \
	libgeoip-dev \
	ffmpeg \
	python3 \
	python3-pip \
	python3-dev \
	portaudio19-dev \
	libasound2-dev \
	libsndfile1-dev \
	pulseaudio \
	alsa-utils \
	&& rm -rf /var/lib/apt/lists/*

# Install Go
RUN wget https://go.dev/dl/go1.22.2.linux-amd64.tar.gz \
	&& tar -C /usr/local -xzf go1.22.2.linux-amd64.tar.gz \
	&& rm go1.22.2.linux-amd64.tar.gz

# install WhisperLiveKit
RUN pip install --upgrade pip
RUN pip install torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cu121
RUN pip install mosestokenizer wtpsplit
RUN pip install diart
RUN pip install whisperlivekit
RUN pip install huggingface_hub

# Build nginx with RTMP module
WORKDIR /tmp

# Download nginx source
RUN curl -L https://nginx.org/download/nginx-${NGINX_VERSION}.tar.gz | tar -xz

# Download nginx-rtmp-module
RUN git clone https://github.com/arut/nginx-rtmp-module.git

# Build nginx with RTMP module
WORKDIR /tmp/nginx-${NGINX_VERSION}
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
	--with-http_ssl_module \
	--with-http_realip_module \
	--with-http_addition_module \
	--with-http_sub_module \
	--with-http_dav_module \
	--with-http_flv_module \
	--with-http_mp4_module \
	--with-http_gunzip_module \
	--with-http_gzip_static_module \
	--with-http_random_index_module \
	--with-http_secure_link_module \
	--with-http_stub_status_module \
	--with-http_auth_request_module \
	--with-http_xslt_module=dynamic \
	--with-http_image_filter_module=dynamic \
	--with-http_geoip_module=dynamic \
	--with-threads \
	--with-stream \
	--with-stream_ssl_module \
	--with-stream_ssl_preread_module \
	--with-stream_realip_module \
	--with-stream_geoip_module=dynamic \
	--with-http_slice_module \
	--with-http_v2_module \
	--add-module=/tmp/nginx-rtmp-module \
	&& make -j$(nproc) \
	&& make install

# Create nginx user and necessary directories
RUN mkdir -p /var/cache/nginx \
	&& mkdir -p /var/log/nginx \
	&& mkdir -p /usr/share/nginx/html

# Copy nginx configuration and HTML files
COPY nginx.conf /etc/nginx/nginx.conf
RUN mkdir -p /usr/share/nginx/html
COPY index.html /usr/share/nginx/html/index.html

# Clean up build dependencies and temporary files
WORKDIR /
RUN rm -rf /tmp/*

# Create a working directory for the application
WORKDIR /app

# Copy Go application files
COPY go.mod go.sum* ./
COPY *.go ./

# Download Go dependencies
RUN go mod download

# Build Go application
RUN go build -o sub-proxy .

# Copy container startup script
COPY container_start.sh /app/container_start.sh
RUN chmod +x /app/container_start.sh

# Expose ports
EXPOSE 80 1935 8000

# Default command - run startup script
CMD ["/app/container_start.sh"]
