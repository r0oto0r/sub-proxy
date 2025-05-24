#!/bin/bash

# Stream processing with transcription and translation to Twitch
# Usage: ./process_stream.sh <stream_name>

echo "Starting stream processing for Twitch..."
if [ -z "$1" ]; then
	echo "Usage: $0 <stream_name>"
	exit 1
fi

STREAM_NAME="$1"

gst-launch-1.0 \
    rtmpsrc location="rtmp://localhost:1935/live/${STREAM_NAME}" ! \
    flvdemux name=demux \
    \
    demux.video ! queue ! \
    h264parse ! \
    video/x-h264 ! \
    tee name=video_tee \
    \
    demux.audio ! queue ! \
    aacparse ! \
    audio/mpeg,mpegversion=4 ! \
    tee name=audio_tee \
    \
    audio_tee. ! queue name=whisper_queue ! \
    avdec_aac ! \
    audioconvert ! \
    audioresample ! \
    audio/x-raw,format=S16LE,rate=16000,channels=1 ! \
    whisper ! \
    overlay.text_sink \
    \
    video_tee. ! queue name=video_queue ! \
    overlay.video_sink \
    \
    textoverlay \
        name=overlay \
        font-desc="Sans Bold 24" \
        valignment=bottom \
        halignment=center \
        shaded-background=true \
        color=0xFFFFFFFF \
        auto-resize=false \
        line-alignment=center ! \
    queue name=encode_queue ! \
    x264enc \
        bitrate=3000 \
        speed-preset=fast \
        tune=zerolatency \
        key-int-max=60 ! \
    video/x-h264,profile=main ! \
    h264parse ! \
    flvmux name=mux \
    \
    audio_tee. ! queue name=audio_passthrough_queue ! \
    voaacenc bitrate=128000 ! \
    aacparse ! \
    audio/mpeg,mpegversion=4 ! \
    mux.audio \
    \
    mux.src ! queue ! \
    rtmpsink location="rtmp://live.twitch.tv/live/${STREAM_NAME}"
