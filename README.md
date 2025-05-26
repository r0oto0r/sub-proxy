# Sub-Proxy: Real-time Audio Transcription System

This system provides real-time audio transcription for RTMP streams using WhisperLiveKit and a Go-based audio streaming application.

## Architecture

1. **NGINX with RTMP Module**: Receives RTMP streams on port 1935
2. **Go Audio Streamer**: Extracts audio from RTMP streams using FFmpeg and sends it to WhisperLiveKit via WebSocket
3. **WhisperLiveKit Server**: Provides real-time transcription using OpenAI's Whisper models
4. **Transcript Processor**: Processes transcription results (stub implementation for extensibility)

## Components

- **NGINX**: Handles RTMP streaming on port 1935
- **WhisperLiveKit**: Real-time transcription server on port 8000
- **Go Application**: Audio processing and WebSocket communication
- **Web Interface**: Basic web interface on port 80

## Usage

### Building and Running

```bash
# Build and start the container
docker-compose up --build

# Or with detached mode
docker-compose up --build -d
```

### Streaming

1. Stream to: `rtmp://localhost:1935/live/YOUR_STREAM_NAME`
2. The Go application will automatically:
   - Extract audio from the stream
   - Convert it to the format required by WhisperLiveKit
   - Send audio chunks via WebSocket to WhisperLiveKit
   - Process transcription responses

### Ports

- `8080`: Web interface
- `1935`: RTMP streaming
- `8000`: WhisperLiveKit WebSocket API

## Configuration

### WhisperLiveKit Model

The default model is `tiny.en`. You can modify this in `container_start.sh`:

```bash
python3 -m whisperlivekit.server --model base.en --host 0.0.0.0 --port 8000 &
```

Available models: `tiny.en`, `base.en`, `small.en`, `medium.en`, `large-v2`, `large-v3`

### Audio Processing

The Go application is configured for:
- Sample rate: 16kHz (required by Whisper)
- Format: 16-bit PCM mono (required by WhisperLiveKit protocol)
- Chunk size: 1KB
- Protocol: JSON messages with base64-encoded audio data

## Extensibility

### Transcript Processor

The `TranscriptProcessor` is designed as a stub for easy extension. You can implement:

- Database storage
- Real-time overlay systems
- WebSocket broadcasting to clients
- Language processing and filtering
- Translation services
- Sentiment analysis
- Keyword detection and alerts

### Example Extension

```go
func (tp *TranscriptProcessor) ProcessTranscript(transcript string, streamName string) {
    // Store in database
    tp.storeInDatabase(transcript, streamName)
    
    // Broadcast to WebSocket clients
    tp.broadcastToClients(transcript, streamName)
    
    // Send to overlay system
    tp.sendToOverlay(transcript, streamName)
}
```

## Requirements

- Docker with NVIDIA runtime (for GPU acceleration)
- NVIDIA GPU with CUDA 12.9 support
- Sufficient GPU memory for the selected Whisper model

## Development

The system is designed for easy development and extension:

1. Modify the Go code in `main.go` or `transcript_processor.go`
2. Rebuild the container: `docker-compose up --build`
3. The application will automatically handle new streams

## Logs

View logs from all components:

```bash
docker-compose logs -f
```

View specific component logs:

```bash
docker-compose logs -f sub-proxy
```
