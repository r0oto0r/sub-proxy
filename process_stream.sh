#!/bin/bash

# Configuration
SOURCE_RTMP_URL="rtmp://localhost:1935/live"
TARGET_RTMP_URL="rtmp://localhost:1935/subtitle"
WHISPER_LANGUAGE="de"
TRANSLATE_ENABLED="true"
VIDEO_BITRATE="2000"
AUDIO_BITRATE="128000"
SUBTITLE_FONT="Sans Bold 24"

echo "Starting stream processing for Twitch..."
if [ -z "$1" ]; then
	echo "Usage: $0 <stream_name>"
	echo "Example: $0 my_stream"
	exit 1
fi

STREAM_NAME="$1"

echo "Processing stream: $STREAM_NAME"
echo "Source: ${SOURCE_RTMP_URL}/${STREAM_NAME}"
echo "Target: ${TARGET_RTMP_URL}/${STREAM_NAME}"
echo "Whisper model: $WHISPER_MODEL_PATH"

export GST_DEBUG=3

# Complete GStreamer pipeline with Whisper transcription/translation and subtitle burning
gst-launch-1.0 -v \
    rtmpsrc location="${SOURCE_RTMP_URL}/${STREAM_NAME}" ! \
    flvdemux name=demux \
    demux.video ! queue ! h264parse ! avdec_h264 ! \
    textoverlay name=overlay font-desc="${SUBTITLE_FONT}" valignment=bottom halignment=center ! \
    videoconvert ! x264enc bitrate=${VIDEO_BITRATE} speed-preset=fast tune=zerolatency ! \
    h264parse ! flvmux name=mux streamable=true ! \
    rtmpsink location="${TARGET_RTMP_URL}/${STREAM_NAME}" \
    demux.audio ! queue ! aacparse ! avdec_aac ! audioconvert ! audioresample ! \
    tee name=t \
    t. ! queue ! whisper language="${WHISPER_LANGUAGE}" translate=${TRANSLATE_ENABLED} ! \
    text/x-raw ! overlay.text_sink \
    t. ! queue ! audioconvert ! avenc_aac bitrate=${AUDIO_BITRATE} ! queue ! mux.

echo "Pipeline finished. Exit code: $?"

