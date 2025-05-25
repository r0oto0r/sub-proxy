package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// AudioStreamer handles audio streaming to WhisperLiveKit
type AudioStreamer struct {
	streamName         string
	whisperURL         string
	conn               *websocket.Conn
	processor          *TranscriptProcessor
	ctx                context.Context
	cancel             context.CancelFunc
	ffmpegCmd          *exec.Cmd
	ffmpegStdout       io.ReadCloser
	audioBuffer        chan []byte
	chunkDuration      time.Duration // Duration of each audio chunk (like browser)
	sampleRate         int
	ffmpegRestartCount int
	transcriptErrors   int
	maxRestarts        int
	restartDelay       time.Duration
}

func NewAudioStreamer(streamName, whisperURL string, processor *TranscriptProcessor) *AudioStreamer {
	ctx, cancel := context.WithCancel(context.Background())

	return &AudioStreamer{
		streamName:         streamName,
		whisperURL:         whisperURL,
		processor:          processor,
		ctx:                ctx,
		cancel:             cancel,
		audioBuffer:        make(chan []byte, 100),
		chunkDuration:      1 * time.Second, // 1 second chunks like browser default
		sampleRate:         16000,           // 16kHz for Whisper
		ffmpegRestartCount: 0,
		transcriptErrors:   0,
		maxRestarts:        10, // Maximum restarts before giving up
		restartDelay:       2 * time.Second,
	}
}

func (as *AudioStreamer) Start() error {
	log.Printf("Starting audio streamer for stream: %s", as.streamName)

	// Connect to WhisperLiveKit WebSocket with retry
	if err := as.connectToWhisperWithRetry(); err != nil {
		return fmt.Errorf("failed to connect to WhisperLiveKit after retries: %v", err)
	}

	// Start FFmpeg process to extract audio with auto-restart
	go as.startFFmpegWithRestart()

	// Start goroutines for processing
	go as.readAudioFromFFmpeg()
	go as.sendAudioToWhisper()
	go as.receiveTranscriptionsWithRetry()

	return nil
}

func (as *AudioStreamer) connectToWhisper() error {
	u := url.URL{Scheme: "ws", Host: as.whisperURL, Path: "/asr"}
	log.Printf("Connecting to WhisperLiveKit at %s", u.String())

	// Set headers for WhisperLiveKit compatibility (match browser behavior)
	headers := make(map[string][]string)
	headers["User-Agent"] = []string{"SubProxy-AudioStreamer/1.0"}
	headers["Sec-WebSocket-Protocol"] = []string{""}

	// Create dialer with proper configuration
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second
	dialer.ReadBufferSize = 1024 * 64  // 64KB read buffer for WebM chunks
	dialer.WriteBufferSize = 1024 * 64 // 64KB write buffer for WebM chunks

	var err error
	as.conn, _, err = dialer.Dial(u.String(), headers)
	if err != nil {
		return err
	}

	// Set WebSocket timeouts to match browser behavior
	as.conn.SetReadDeadline(time.Time{})  // No read deadline (browser doesn't set one)
	as.conn.SetWriteDeadline(time.Time{}) // No write deadline

	log.Printf("Connected to WhisperLiveKit WebSocket")

	// Note: WhisperLiveKit doesn't require initial configuration like some other services
	// The browser client just starts sending audio data immediately
	return nil
}

func (as *AudioStreamer) connectToWhisperWithRetry() error {
	maxRetries := 5
	for i := 0; i < maxRetries; i++ {
		if err := as.connectToWhisper(); err != nil {
			log.Printf("Failed to connect to WhisperLiveKit (attempt %d/%d): %v", i+1, maxRetries, err)
			if i < maxRetries-1 {
				time.Sleep(as.restartDelay)
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("failed to connect after %d attempts", maxRetries)
}

func (as *AudioStreamer) startFFmpeg() error {
	rtmpURL := fmt.Sprintf("rtmp://localhost:1935/live/%s", as.streamName)

	// FFmpeg command to extract audio and convert to webm format for WhisperLiveKit
	// Use a simpler approach that creates properly chunked WebM output
	args := []string{
		"-i", rtmpURL,
		"-vn",                // No video
		"-acodec", "libopus", // Opus codec (same as browser MediaRecorder)
		"-ar", fmt.Sprintf("%d", as.sampleRate), // Sample rate (16kHz)
		"-ac", "1", // Mono audio
		"-b:a", "128k", // Audio bitrate
		"-f", "webm", // WebM container
		"-cluster_size_limit", "2M", // Limit cluster size for better chunking
		"-cluster_time_limit", "1000", // 1 second clusters (like browser default)
		"-", // Output to stdout
	}

	as.ffmpegCmd = exec.CommandContext(as.ctx, "ffmpeg", args...)

	stdout, err := as.ffmpegCmd.StdoutPipe()
	if err != nil {
		return err
	}

	stderr, err := as.ffmpegCmd.StderrPipe()
	if err != nil {
		return err
	}

	// Log FFmpeg errors
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stderr.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("FFmpeg stderr error: %v", err)
				}
				return
			}
			log.Printf("FFmpeg: %s", string(buf[:n]))
		}
	}()

	if err := as.ffmpegCmd.Start(); err != nil {
		return err
	}

	// Store stdout pipe for reading audio data
	as.ffmpegStdout = stdout

	log.Printf("FFmpeg started for stream: %s", as.streamName)
	return nil
}

func (as *AudioStreamer) startFFmpegWithRestart() {
	for {
		select {
		case <-as.ctx.Done():
			return
		default:
			if as.ffmpegRestartCount >= as.maxRestarts {
				log.Printf("FFmpeg failed too many times (%d), giving up", as.ffmpegRestartCount)
				return
			}

			log.Printf("Starting FFmpeg (attempt %d)", as.ffmpegRestartCount+1)
			if err := as.startFFmpeg(); err != nil {
				log.Printf("Failed to start FFmpeg: %v", err)
				as.ffmpegRestartCount++
				time.Sleep(as.restartDelay)
				continue
			}

			// Wait for FFmpeg to finish or fail
			if as.ffmpegCmd != nil {
				err := as.ffmpegCmd.Wait()
				if err != nil {
					log.Printf("FFmpeg exited with error: %v, restarting...", err)
					as.ffmpegRestartCount++
					time.Sleep(as.restartDelay)
					continue
				}
			}

			// If we get here, FFmpeg exited cleanly, which shouldn't happen in normal operation
			log.Printf("FFmpeg exited unexpectedly, restarting...")
			as.ffmpegRestartCount++
			time.Sleep(as.restartDelay)
		}
	}
}

func (as *AudioStreamer) sendStopSignal() {
	if as.conn != nil {
		// Send empty message as stop signal (like the browser client does)
		log.Printf("Sending stop signal to WhisperLiveKit")
		if err := as.conn.WriteMessage(websocket.BinaryMessage, []byte{}); err != nil {
			log.Printf("Error sending stop signal: %v", err)
		}
	}
}

func (as *AudioStreamer) readAudioFromFFmpeg() {
	defer close(as.audioBuffer)

	// Buffer to accumulate WebM data
	var webmBuffer []byte
	buffer := make([]byte, 4096) // Smaller read buffer
	lastSendTime := time.Now()
	sendInterval := as.chunkDuration // Use configured chunk duration

	for {
		select {
		case <-as.ctx.Done():
			// Send any remaining data before stopping
			if len(webmBuffer) > 0 {
				chunk := make([]byte, len(webmBuffer))
				copy(chunk, webmBuffer)
				select {
				case as.audioBuffer <- chunk:
				default:
				}
			}
			// Send stop signal to WhisperLiveKit before closing
			go as.sendStopSignal()
			return
		default:
			if as.ffmpegStdout == nil {
				log.Printf("FFmpeg stdout is nil, waiting for restart...")
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Set read timeout to avoid blocking indefinitely
			if conn, ok := as.ffmpegStdout.(*os.File); ok {
				conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			}

			n, err := as.ffmpegStdout.Read(buffer)
			if err != nil {
				if err != io.EOF && !os.IsTimeout(err) {
					log.Printf("Error reading from FFmpeg: %v", err)
				}
				// Check if it's time to send accumulated data
				if time.Since(lastSendTime) >= sendInterval && len(webmBuffer) > 0 {
					chunk := make([]byte, len(webmBuffer))
					copy(chunk, webmBuffer)
					select {
					case as.audioBuffer <- chunk:
						webmBuffer = webmBuffer[:0] // Reset buffer
						lastSendTime = time.Now()
					case <-as.ctx.Done():
						go as.sendStopSignal()
						return
					default:
						// Buffer full, skip
					}
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}

			if n > 0 {
				// Accumulate WebM data
				webmBuffer = append(webmBuffer, buffer[:n]...)

				// Send chunk when we have enough data OR enough time has passed
				// This mimics how browser MediaRecorder works
				if time.Since(lastSendTime) >= sendInterval && len(webmBuffer) > 0 {
					chunk := make([]byte, len(webmBuffer))
					copy(chunk, webmBuffer)

					select {
					case as.audioBuffer <- chunk:
						webmBuffer = webmBuffer[:0] // Reset buffer
						lastSendTime = time.Now()
					case <-as.ctx.Done():
						go as.sendStopSignal()
						return
					default:
						// Buffer full, skip this chunk
						log.Printf("Audio buffer full, skipping chunk")
					}
				}

				// Also limit buffer size to prevent unbounded growth
				if len(webmBuffer) > 256*1024 { // 256KB max buffer
					log.Printf("WebM buffer too large (%d bytes), forcing send", len(webmBuffer))
					chunk := make([]byte, len(webmBuffer))
					copy(chunk, webmBuffer)

					select {
					case as.audioBuffer <- chunk:
						webmBuffer = webmBuffer[:0] // Reset buffer
						lastSendTime = time.Now()
					case <-as.ctx.Done():
						go as.sendStopSignal()
						return
					default:
						// Buffer full, reset anyway to prevent memory issues
						webmBuffer = webmBuffer[:0]
						log.Printf("Forced buffer reset due to full channel")
					}
				}
			}
		}
	}
}

func (as *AudioStreamer) sendAudioToWhisper() {
	chunkCount := 0
	for {
		select {
		case <-as.ctx.Done():
			// Send stop signal when context is cancelled
			as.sendStopSignal()
			return
		case audioChunk, ok := <-as.audioBuffer:
			if !ok {
				// Channel closed, send stop signal
				as.sendStopSignal()
				return
			}

			// Skip if no connection
			if as.conn == nil {
				log.Printf("No WhisperLiveKit connection, skipping audio chunk")
				continue
			}

			chunkCount++

			// Log chunk info periodically (every 10th chunk to avoid spam)
			if chunkCount%10 == 1 {
				log.Printf("Sending WebM chunk #%d (size: %d bytes) to WhisperLiveKit", chunkCount, len(audioChunk))
			}

			// Send raw WebM bytes to WhisperLiveKit (same as browser)
			if err := as.conn.WriteMessage(websocket.BinaryMessage, audioChunk); err != nil {
				log.Printf("Error sending audio to WhisperLiveKit: %v", err)
				// Don't return here, let the connection retry logic handle it
				continue
			}
		}
	}
}

func (as *AudioStreamer) receiveTranscriptions() error {
	messageCount := 0
	for {
		select {
		case <-as.ctx.Done():
			return nil
		default:
			_, message, err := as.conn.ReadMessage()
			if err != nil {
				return fmt.Errorf("error reading from WhisperLiveKit: %v", err)
			}

			messageCount++

			// Process the transcription response (JSON format)
			transcript := string(message)

			// Log detailed message info for debugging
			log.Printf("[%s] Message #%d from WhisperLiveKit (length: %d bytes)", as.streamName, messageCount, len(message))
			log.Printf("[%s] Raw WebSocket message: %s", as.streamName, transcript)

			// Check for special server messages
			var response map[string]interface{}
			if err := json.Unmarshal(message, &response); err == nil {
				if msgType, ok := response["type"].(string); ok && msgType == "ready_to_stop" {
					log.Printf("[%s] Received ready_to_stop signal from WhisperLiveKit", as.streamName)
					return nil // Exit gracefully
				}

				// Log message structure for debugging
				if msgType, ok := response["type"].(string); ok {
					log.Printf("[%s] Message type: %s", as.streamName, msgType)
				}
				if lines, ok := response["lines"].([]interface{}); ok {
					log.Printf("[%s] Lines count: %d", as.streamName, len(lines))
				}
			} else {
				log.Printf("[%s] Failed to parse JSON response: %v", as.streamName, err)
			}

			// Process transcript in a separate goroutine to avoid blocking
			go func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("Panic in transcript processing: %v", r)
					}
				}()
				as.processor.ProcessTranscript(transcript, as.streamName)
			}()
		}
	}
}

func (as *AudioStreamer) receiveTranscriptionsWithRetry() {
	for {
		select {
		case <-as.ctx.Done():
			return
		default:
			if as.transcriptErrors >= as.maxRestarts {
				log.Printf("Transcription service failed too many times (%d), giving up", as.transcriptErrors)
				return
			}

			// Try to receive transcriptions
			if err := as.receiveTranscriptions(); err != nil {
				log.Printf("Transcription error: %v, restarting in %v...", err, as.restartDelay)
				as.transcriptErrors++

				// Close current connection and reconnect
				if as.conn != nil {
					as.conn.Close()
				}

				time.Sleep(as.restartDelay)

				// Try to reconnect to Whisper
				if err := as.connectToWhisperWithRetry(); err != nil {
					log.Printf("Failed to reconnect to WhisperLiveKit: %v", err)
					continue
				}

				// Reset error count on successful reconnection
				as.transcriptErrors = 0
				log.Printf("Successfully reconnected to WhisperLiveKit")
				continue
			}

			// If receiveTranscriptions returns without error, it means it exited cleanly
			log.Printf("Transcription service exited cleanly, restarting...")
			time.Sleep(as.restartDelay)
		}
	}
}

func (as *AudioStreamer) Stop() {
	log.Printf("Stopping audio streamer for stream: %s", as.streamName)

	// Send stop signal before closing
	as.sendStopSignal()

	as.cancel()

	if as.ffmpegCmd != nil && as.ffmpegCmd.Process != nil {
		as.ffmpegCmd.Process.Kill()
	}

	if as.conn != nil {
		// Give a moment for the stop signal to be sent
		time.Sleep(100 * time.Millisecond)
		as.conn.Close()
	}
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: audio-streamer <stream_name>")
	}

	streamName := os.Args[1]
	whisperURL := "localhost:8000" // WhisperLiveKit default port

	log.Printf("Starting audio streamer for stream: %s", streamName)
	log.Printf("YouTube forwarding will be handled by nginx with 10 second buffer")

	// Create transcript processor
	processor := NewTranscriptProcessor()

	// Create audio streamer
	streamer := NewAudioStreamer(streamName, whisperURL, processor)

	// Handle shutdown gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start the streamer
	if err := streamer.Start(); err != nil {
		log.Fatalf("Failed to start audio streamer: %v", err)
	}

	// Wait for shutdown signal
	<-sigCh
	log.Printf("Received shutdown signal, stopping...")

	streamer.Stop()
	log.Printf("Audio streamer stopped")
}
