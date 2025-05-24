# Use Alpine Linux base image
FROM alpine:3.20

# Set environment variables
ENV NGINX_VERSION=1.24.0
ENV RTMP_MODULE_VERSION=1.2.2

# Install system dependencies
RUN apk add --no-cache \
	git \
	curl \
	build-base \
	cmake \
	pkgconfig \
	openssl-dev \
	zlib-dev \
	pcre-dev \
	libxml2-dev \
	libxslt-dev \
	gd-dev \
	geoip-dev \
	ffmpeg \
	gstreamer \
	gst-plugins-base \
	gst-plugins-good \
	gst-plugins-bad \
	gst-plugins-ugly \
	gst-libav \
	gstreamer-dev \
	gst-plugins-base-dev \
	glib-dev \
	cairo-dev \
	pango-dev \
	&& rm -rf /var/cache/apk/*

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
RUN addgroup -g 101 -S nginx \
	&& adduser -S -D -H -u 101 -h /var/cache/nginx -s /sbin/nologin -G nginx -g nginx nginx \
	&& mkdir -p /var/cache/nginx \
	&& mkdir -p /var/log/nginx \
	&& mkdir -p /usr/share/nginx/html \
	&& chown -R nginx:nginx /var/cache/nginx /var/log/nginx

# Copy nginx configuration and HTML files
COPY nginx.conf /etc/nginx/nginx.conf
RUN mkdir -p /usr/share/nginx/html
COPY index.html /usr/share/nginx/html/index.html

# Clean up build dependencies and temporary files
WORKDIR /
RUN rm -rf /tmp/*

# Create a working directory for the application
WORKDIR /app

# Copy and make stream processing script executable
COPY process_stream.sh /app/process_stream.sh
RUN chmod +x /app/process_stream.sh

# Copy container startup script
COPY container_start.sh /app/container_start.sh
RUN chmod +x /app/container_start.sh

# Expose ports
EXPOSE 80 1935

# Default command - run startup script
CMD ["/app/container_start.sh"]
