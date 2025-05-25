package main

import (
	"context"
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
		chunkSize:          1024,  // 1KB chunks
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

	var err error
	as.conn, _, err = websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}

	log.Printf("Connected to WhisperLiveKit WebSocket")
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

	// FFmpeg command to extract audio and convert to raw PCM
	args := []string{
		"-i", rtmpURL,
		"-vn",                  // No video
		"-acodec", "pcm_s16le", // 16-bit PCM little endian
		"-ar", fmt.Sprintf("%d", as.sampleRate), // Sample rate
		"-ac", "1", // Mono
		"-f", "s16le", // Raw 16-bit little endian format
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

func (as *AudioStreamer) readAudioFromFFmpeg() {
	defer close(as.audioBuffer)

	buffer := make([]byte, as.chunkSize*2) // 2 bytes per sample for 16-bit

	for {
		select {
		case <-as.ctx.Done():
			return
		default:
			if as.ffmpegStdout == nil {
				log.Printf("FFmpeg stdout is nil, waiting for restart...")
				time.Sleep(100 * time.Millisecond)
				continue
			}

			n, err := as.ffmpegStdout.Read(buffer)
			if err != nil {
				if err != io.EOF {
					log.Printf("Error reading from FFmpeg: %v", err)
				}
				// Wait a bit before trying again (FFmpeg might be restarting)
				time.Sleep(100 * time.Millisecond)
				continue
			}

			if n > 0 {
				// Send audio chunk to buffer
				chunk := make([]byte, n)
				copy(chunk, buffer[:n])

				select {
				case as.audioBuffer <- chunk:
				case <-as.ctx.Done():
					return
				default:
					// Buffer full, skip this chunk
					log.Printf("Audio buffer full, skipping chunk")
				}
			}
		}
	}
}

func (as *AudioStreamer) sendAudioToWhisper() {
	for {
		select {
		case <-as.ctx.Done():
			return
		case audioChunk, ok := <-as.audioBuffer:
			if !ok {
				return
			}

			// Skip if no connection
			if as.conn == nil {
				log.Printf("No WhisperLiveKit connection, skipping audio chunk")
				continue
			}

			// Send raw audio bytes to WhisperLiveKit
			if err := as.conn.WriteMessage(websocket.BinaryMessage, audioChunk); err != nil {
				log.Printf("Error sending audio to WhisperLiveKit: %v", err)
				// Don't return here, let the connection retry logic handle it
				continue
			}
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

			// Process the transcription response
			transcript := string(message)

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

	as.cancel()

	if as.ffmpegCmd != nil && as.ffmpegCmd.Process != nil {
		as.ffmpegCmd.Process.Kill()
	}

	if as.conn != nil {
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
