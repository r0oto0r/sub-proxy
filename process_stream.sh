#!/bin/bash

# Stream processing with transcription and translation to Twitch
# Usage: ./process_stream.sh <stream_name>

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
    audio_tee. ! queue ! \
    transcribebin \
        name=transcriber \
        language-code=de-DE \
        latency=1000 \
        passthrough=true \
        transcriber=whisper \
        model=large-v3 \
        translate=true \
        translate-src-language=de \
        translate-target-language=en ! \
    \
    textoverlay \
        name=overlay \
        font-desc="Sans Bold 24" \
        valignment=bottom \
        halignment=center \
        shaded-background=true \
        color=0xFFFFFFFF \
        auto-resize=false \
        wrap-mode=word-char \
        line-alignment=center ! \
    \
    video_tee. ! queue ! \
    overlay.video_sink \
    \
    overlay.src ! queue ! \
    x264enc \
        bitrate=3000 \
        speed-preset=fast \
        tune=zerolatency \
        key-int-max=60 ! \
    video/x-h264,profile=main ! \
    h264parse ! \
    flvmux name=mux \
    \
    audio_tee. ! queue ! \
    voaacenc bitrate=128000 ! \
    aacparse ! \
    audio/mpeg,mpegversion=4 ! \
    mux.audio \
    \
    mux.src ! queue ! \
    rtmpsink location="rtmp://live.twitch.tv/live/${STREAM_NAME}"
