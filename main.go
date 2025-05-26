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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// StreamManager manages active streams
type StreamManager struct {
	streams         map[string]*AudioStreamer
	mutex           sync.RWMutex
	globalProcessor *TranscriptProcessor // Shared processor for YouTube captions configuration
}

func NewStreamManager() *StreamManager {
	return &StreamManager{
		streams:         make(map[string]*AudioStreamer),
		globalProcessor: NewTranscriptProcessor(),
	}
}

func (sm *StreamManager) StartStream(streamName string) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if _, exists := sm.streams[streamName]; exists {
		return fmt.Errorf("stream %s already running", streamName)
	}

	whisperURL := "localhost:8000"
	streamer := NewAudioStreamer(streamName, whisperURL, sm.globalProcessor)

	if err := streamer.Start(); err != nil {
		return fmt.Errorf("failed to start stream %s: %v", streamName, err)
	}

	// Auto-configure YouTube captions if CID is available from environment
	if cid := os.Getenv("YOUTUBE_CID"); cid != "" {
		sm.globalProcessor.ConfigureYouTubeCaptions(streamName, cid)
		log.Printf("Auto-configured YouTube captions for stream %s with CID from env", streamName)
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
func (sm *StreamManager) waitForWhisperLiveKit() error {
	whisperURL := "http://0.0.0.0:8000"
	timeout := 300 * time.Second // Wait up to 300 seconds
	interval := 1 * time.Second
	start := time.Now()

	log.Printf("Waiting for WhisperLiveKit to become available at %s...", whisperURL)

	for time.Since(start) < timeout {
		client := &http.Client{
			Timeout: 2 * time.Second,
		}

		resp, err := client.Get(whisperURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				log.Printf("WhisperLiveKit is now available at %s", whisperURL)
				return nil
			}
		}

		log.Printf("WhisperLiveKit not yet available, retrying in %v...", interval)
		time.Sleep(interval)
	}

	return fmt.Errorf("WhisperLiveKit did not become available within %v", timeout)
}

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

	// Wait for WhisperLiveKit to become available before allowing publish
	if err := sm.waitForWhisperLiveKit(); err != nil {
		log.Printf("WhisperLiveKit not available for stream %s: %v", streamName, err)
		http.Error(w, "Transcription service not available", http.StatusServiceUnavailable)
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
	isConnected        bool
	connMutex          sync.Mutex
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
		isConnected:        false,
	}
}

func (as *AudioStreamer) Start() error {
	log.Printf("Starting audio streamer for stream: %s", as.streamName)

	// Don't connect to WhisperLiveKit immediately - wait for audio data
	// Start FFmpeg process to extract audio with auto-restart
	go as.startFFmpegWithRestart()

	// Start goroutines for processing
	go as.readAudioFromFFmpeg()
	go as.sendAudioToWhisper()

	return nil
}

func (as *AudioStreamer) connectToWhisperIfNeeded() error {
	as.connMutex.Lock()
	defer as.connMutex.Unlock()

	if as.isConnected && as.conn != nil {
		return nil // Already connected
	}

	log.Printf("[%s] Connecting to WhisperLiveKit for the first time (stream is now active)", as.streamName)
	if err := as.connectToWhisperWithRetry(); err != nil {
		return fmt.Errorf("failed to connect to WhisperLiveKit: %v", err)
	}

	as.isConnected = true
	// Start receiving transcriptions once connected
	go as.receiveTranscriptionsWithRetry()
	log.Printf("[%s] Successfully connected to WhisperLiveKit and started transcription service", as.streamName)
	return nil
}

func (as *AudioStreamer) disconnectFromWhisper() {
	as.connMutex.Lock()
	defer as.connMutex.Unlock()

	if as.conn != nil {
		log.Printf("[%s] Disconnecting from WhisperLiveKit", as.streamName)
		as.sendStopSignal()
		time.Sleep(100 * time.Millisecond) // Give time for stop signal
		as.conn.Close()
		as.conn = nil
	}
	as.isConnected = false
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

			// Clean up old FFmpeg process and stdout reference before starting new one
			if as.ffmpegCmd != nil && as.ffmpegCmd.Process != nil {
				as.ffmpegCmd.Process.Kill()
			}
			if as.ffmpegStdout != nil {
				as.ffmpegStdout.Close()
				as.ffmpegStdout = nil
			}

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
					// Clean up stdout reference when FFmpeg fails
					if as.ffmpegStdout != nil {
						as.ffmpegStdout.Close()
						as.ffmpegStdout = nil
					}
					as.ffmpegRestartCount++
					time.Sleep(as.restartDelay)
					continue
				}
			}

			// If we get here, FFmpeg exited cleanly, which shouldn't happen in normal operation
			log.Printf("FFmpeg exited unexpectedly, restarting...")
			// Clean up stdout reference when FFmpeg exits unexpectedly
			if as.ffmpegStdout != nil {
				as.ffmpegStdout.Close()
				as.ffmpegStdout = nil
			}
			as.ffmpegRestartCount++
			time.Sleep(as.restartDelay)
		}
	}
}

func (as *AudioStreamer) sendStopSignal() {
	as.connMutex.Lock()
	defer as.connMutex.Unlock()

	if as.conn != nil {
		// Send empty message as stop signal (like the browser client does)
		log.Printf("[%s] Sending stop signal to WhisperLiveKit", as.streamName)
		if err := as.conn.WriteMessage(websocket.BinaryMessage, []byte{}); err != nil {
			log.Printf("[%s] Error sending stop signal: %v", as.streamName, err)
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
					// Check if this is a "file already closed" error or similar pipe closure
					if strings.Contains(err.Error(), "file already closed") ||
						strings.Contains(err.Error(), "broken pipe") ||
						strings.Contains(err.Error(), "use of closed") {
						log.Printf("[%s] readAudioFromFFmpeg: FFmpeg pipe closed, clearing stdout reference", as.streamName)
						as.ffmpegStdout = nil
						time.Sleep(100 * time.Millisecond)
						continue
					}
					log.Printf("[%s] readAudioFromFFmpeg: Error reading from FFmpeg: %v", as.streamName, err)
				} else {
					log.Printf("[%s] readAudioFromFFmpeg: FFmpeg stdout EOF", as.streamName)
				}
				// Clear the stdout reference and wait for restart
				as.ffmpegStdout = nil
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
			as.disconnectFromWhisper()
			return
		case audioChunk, ok := <-as.audioBuffer:
			if !ok {
				// Channel closed, send stop signal
				log.Printf("[%s] sendAudioToWhisper: Audio buffer channel closed, sending stop signal", as.streamName)
				as.disconnectFromWhisper()
				return
			}

			chunkCount++
			log.Printf("[%s] sendAudioToWhisper: Received audio chunk #%d with %d bytes from buffer", as.streamName, chunkCount, len(audioChunk))

			// Connect to WhisperLiveKit only when we have audio data
			if err := as.connectToWhisperIfNeeded(); err != nil {
				log.Printf("[%s] sendAudioToWhisper: Failed to connect to WhisperLiveKit for chunk #%d: %v", as.streamName, chunkCount, err)
				continue
			}

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
				if msgType, ok := response["type"].(string); ok {
					switch msgType {
					case "ready_to_stop":
						log.Printf("[%s] Received ready_to_stop signal from WhisperLiveKit", as.streamName)
						return nil // Exit gracefully - this signals done
					case "done":
						log.Printf("[%s] Received done signal from WhisperLiveKit", as.streamName)
						return nil // Exit gracefully - this signals done
					}
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

				// Close current connection
				as.connMutex.Lock()
				if as.conn != nil {
					as.conn.Close()
					as.conn = nil
				}
				as.isConnected = false
				as.connMutex.Unlock()

				time.Sleep(as.restartDelay)

				// Try to reconnect to Whisper
				if err := as.connectToWhisperWithRetry(); err != nil {
					log.Printf("Failed to reconnect to WhisperLiveKit: %v", err)
					continue
				}

				as.connMutex.Lock()
				as.isConnected = true
				as.connMutex.Unlock()

				// Reset error count on successful reconnection
				as.transcriptErrors = 0
				log.Printf("Successfully reconnected to WhisperLiveKit")
				continue
			}

			// If receiveTranscriptions returns without error, it means it received a done signal
			log.Printf("Received done signal from WhisperLiveKit, disconnecting...")
			as.disconnectFromWhisper()
			return
		}
	}
}

func (as *AudioStreamer) Stop() {
	log.Printf("Stopping audio streamer for stream: %s", as.streamName)

	// Disconnect from WhisperLiveKit gracefully
	as.disconnectFromWhisper()

	as.cancel()

	if as.ffmpegCmd != nil && as.ffmpegCmd.Process != nil {
		as.ffmpegCmd.Process.Kill()
	}

	// Clean up stdout reference
	if as.ffmpegStdout != nil {
		as.ffmpegStdout.Close()
		as.ffmpegStdout = nil
	}
}

func main() {
	log.Printf("Starting Sub-Proxy HTTP server...")

	streamManager := NewStreamManager()

	// Setup HTTP routes
	http.HandleFunc("/rtmp_publish", streamManager.handlePublish)
	http.HandleFunc("/rtmp_done", streamManager.handleDone)
	http.HandleFunc("/rtmp_play", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

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
