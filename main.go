package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// StreamManager manages active streams
type StreamManager struct {
	streams map[string]*AudioStreamer
	mutex   sync.RWMutex
}

func NewStreamManager() *StreamManager {
	return &StreamManager{
		streams: make(map[string]*AudioStreamer),
	}
}

func (sm *StreamManager) StartStream(streamName string) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if _, exists := sm.streams[streamName]; exists {
		return fmt.Errorf("stream %s already running", streamName)
	}

	whisperURL := "localhost:8000"
	processor := NewTranscriptProcessor()
	streamer := NewAudioStreamer(streamName, whisperURL, processor)

	if err := streamer.Start(); err != nil {
		return fmt.Errorf("failed to start stream %s: %v", streamName, err)
	}

	sm.streams[streamName] = streamer
	log.Printf("Started transcription for stream: %s", streamName)
	return nil
}

func (sm *StreamManager) StopStream(streamName string) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	streamer, exists := sm.streams[streamName]
	if !exists {
		return fmt.Errorf("stream %s not found", streamName)
	}

	streamer.Stop()
	delete(sm.streams, streamName)
	log.Printf("Stopped transcription for stream: %s", streamName)
	return nil
}

// HTTP handlers
func (sm *StreamManager) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	streamName := r.FormValue("name")
	if streamName == "" {
		http.Error(w, "Missing stream name", http.StatusBadRequest)
		return
	}

	if err := sm.StartStream(streamName); err != nil {
		log.Printf("Error starting stream %s: %v", streamName, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Stream started"))
}

func (sm *StreamManager) handleDone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	streamName := r.FormValue("name")
	if streamName == "" {
		http.Error(w, "Missing stream name", http.StatusBadRequest)
		return
	}

	if err := sm.StopStream(streamName); err != nil {
		log.Printf("Error stopping stream %s: %v", streamName, err)
		// Don't return error, stream might already be stopped
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Stream stopped"))
}

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
	chunkSize          int
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
		chunkSize:          4096,  // 4KB chunks for WebM
		sampleRate:         16000, // 16kHz for Whisper
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

	// Set headers for WhisperLiveKit compatibility
	headers := make(map[string][]string)
	headers["User-Agent"] = []string{"SubProxy-AudioStreamer/1.0"}

	// Create dialer with proper configuration
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	var err error
	as.conn, _, err = dialer.Dial(u.String(), headers)
	if err != nil {
		return err
	}

	log.Printf("Connected to WhisperLiveKit WebSocket")

	// Send initial configuration if needed
	// WhisperLiveKit might expect certain initialization parameters
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

	log.Printf("[%s] startFFmpeg: Starting FFmpeg with RTMP URL: %s", as.streamName, rtmpURL)

	// FFmpeg command to extract audio and convert to webm format for WhisperLiveKit
	// Create short duration WebM chunks similar to MediaRecorder
	args := []string{
		"-i", rtmpURL,
		"-vn",                // No video
		"-acodec", "libopus", // Opus codec (preferred by WhisperLiveKit)
		"-ar", fmt.Sprintf("%d", as.sampleRate), // Sample rate
		"-ac", "1", // Mono
		"-f", "webm", // WebM container format
		"-dash", "1", // Enable DASH mode for better chunking
		"-", // Output to stdout
	}

	log.Printf("[%s] startFFmpeg: FFmpeg command: ffmpeg %v", as.streamName, args)

	as.ffmpegCmd = exec.CommandContext(as.ctx, "ffmpeg", args...)

	stdout, err := as.ffmpegCmd.StdoutPipe()
	if err != nil {
		log.Printf("[%s] startFFmpeg: Error creating stdout pipe: %v", as.streamName, err)
		return err
	}

	stderr, err := as.ffmpegCmd.StderrPipe()
	if err != nil {
		log.Printf("[%s] startFFmpeg: Error creating stderr pipe: %v", as.streamName, err)
		return err
	}

	// Log FFmpeg errors
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stderr.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("[%s] FFmpeg stderr error: %v", as.streamName, err)
				}
				return
			}
			log.Printf("[%s] FFmpeg: %s", as.streamName, string(buf[:n]))
		}
	}()

	if err := as.ffmpegCmd.Start(); err != nil {
		log.Printf("[%s] startFFmpeg: Error starting FFmpeg: %v", as.streamName, err)
		return err
	}

	log.Printf("[%s] startFFmpeg: FFmpeg process started successfully with PID %d", as.streamName, as.ffmpegCmd.Process.Pid)

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

	// Use larger buffer for WebM chunks
	buffer := make([]byte, 16384) // 16KB buffer for WebM chunks
	chunkCount := 0

	log.Printf("[%s] readAudioFromFFmpeg: Starting to read audio from FFmpeg", as.streamName)

	for {
		select {
		case <-as.ctx.Done():
			// Send stop signal to WhisperLiveKit before closing
			log.Printf("[%s] readAudioFromFFmpeg: Context cancelled, sending stop signal", as.streamName)
			go as.sendStopSignal()
			return
		default:
			if as.ffmpegStdout == nil {
				log.Printf("[%s] readAudioFromFFmpeg: FFmpeg stdout is nil, waiting for restart...", as.streamName)
				time.Sleep(100 * time.Millisecond)
				continue
			}

			n, err := as.ffmpegStdout.Read(buffer)
			if err != nil {
				if err != io.EOF {
					log.Printf("[%s] readAudioFromFFmpeg: Error reading from FFmpeg: %v", as.streamName, err)
				} else {
					log.Printf("[%s] readAudioFromFFmpeg: FFmpeg stdout EOF", as.streamName)
				}
				// Wait a bit before trying again (FFmpeg might be restarting)
				time.Sleep(100 * time.Millisecond)
				continue
			}

			if n > 0 {
				chunkCount++
				log.Printf("[%s] readAudioFromFFmpeg: Read audio chunk #%d with %d bytes from FFmpeg", as.streamName, chunkCount, n)

				// Send audio chunk to buffer
				chunk := make([]byte, n)
				copy(chunk, buffer[:n])

				select {
				case as.audioBuffer <- chunk:
					log.Printf("[%s] readAudioFromFFmpeg: Successfully buffered audio chunk #%d", as.streamName, chunkCount)
				case <-as.ctx.Done():
					// Send stop signal before exiting
					log.Printf("[%s] readAudioFromFFmpeg: Context cancelled while buffering, sending stop signal", as.streamName)
					go as.sendStopSignal()
					return
				default:
					// Buffer full, skip this chunk
					log.Printf("[%s] readAudioFromFFmpeg: Audio buffer full, skipping chunk #%d", as.streamName, chunkCount)
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
			log.Printf("[%s] sendAudioToWhisper: Context cancelled, sending stop signal", as.streamName)
			as.sendStopSignal()
			return
		case audioChunk, ok := <-as.audioBuffer:
			if !ok {
				// Channel closed, send stop signal
				log.Printf("[%s] sendAudioToWhisper: Audio buffer channel closed, sending stop signal", as.streamName)
				as.sendStopSignal()
				return
			}

			chunkCount++
			log.Printf("[%s] sendAudioToWhisper: Received audio chunk #%d with %d bytes from buffer", as.streamName, chunkCount, len(audioChunk))

			// Skip if no connection
			if as.conn == nil {
				log.Printf("[%s] sendAudioToWhisper: No WhisperLiveKit connection, skipping audio chunk #%d", as.streamName, chunkCount)
				continue
			}

			// Send raw audio bytes to WhisperLiveKit
			log.Printf("[%s] sendAudioToWhisper: Sending audio chunk #%d (%d bytes) to WhisperLiveKit", as.streamName, chunkCount, len(audioChunk))
			if err := as.conn.WriteMessage(websocket.BinaryMessage, audioChunk); err != nil {
				log.Printf("[%s] sendAudioToWhisper: Error sending audio chunk #%d to WhisperLiveKit: %v", as.streamName, chunkCount, err)
				// Don't return here, let the connection retry logic handle it
				continue
			}
			log.Printf("[%s] sendAudioToWhisper: Successfully sent audio chunk #%d to WhisperLiveKit", as.streamName, chunkCount)
		}
	}
}

func (as *AudioStreamer) receiveTranscriptions() error {
	for {
		select {
		case <-as.ctx.Done():
			return nil
		default:
			_, message, err := as.conn.ReadMessage()
			if err != nil {
				return fmt.Errorf("error reading from WhisperLiveKit: %v", err)
			}

			// Process the transcription response (JSON format)
			transcript := string(message)

			// Log the raw message for debugging
			log.Printf("[%s] Raw WebSocket message: %s", as.streamName, transcript)

			// Check for special server messages
			var response map[string]interface{}
			if err := json.Unmarshal(message, &response); err == nil {
				if msgType, ok := response["type"].(string); ok && msgType == "ready_to_stop" {
					log.Printf("[%s] Received ready_to_stop signal from WhisperLiveKit", as.streamName)
					return nil // Exit gracefully
				}
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
	log.Printf("Starting Sub-Proxy HTTP server...")

	streamManager := NewStreamManager()

	// Setup HTTP routes
	http.HandleFunc("/rtmp_publish", streamManager.handlePublish)
	http.HandleFunc("/rtmp_done", streamManager.handleDone)

	// Start HTTP server
	server := &http.Server{
		Addr:    ":9000",
		Handler: nil,
	}

	go func() {
		log.Printf("HTTP server listening on :9000")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	// Handle shutdown gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for shutdown signal
	<-sigCh
	log.Printf("Received shutdown signal, stopping...")

	// Shutdown HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	log.Printf("Sub-Proxy HTTP server stopped")
}
