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

	"github.com/gorilla/websocket"
)

// AudioStreamer handles audio streaming to WhisperLiveKit
type AudioStreamer struct {
	streamName   string
	whisperURL   string
	conn         *websocket.Conn
	processor    *TranscriptProcessor
	ctx          context.Context
	cancel       context.CancelFunc
	ffmpegCmd    *exec.Cmd
	ffmpegStdout io.ReadCloser
	audioBuffer  chan []byte
	chunkSize    int
	sampleRate   int
}

func NewAudioStreamer(streamName, whisperURL string, processor *TranscriptProcessor) *AudioStreamer {
	ctx, cancel := context.WithCancel(context.Background())

	return &AudioStreamer{
		streamName:  streamName,
		whisperURL:  whisperURL,
		processor:   processor,
		ctx:         ctx,
		cancel:      cancel,
		audioBuffer: make(chan []byte, 100),
		chunkSize:   1024,  // 1KB chunks
		sampleRate:  16000, // 16kHz for Whisper
	}
}

func (as *AudioStreamer) Start() error {
	log.Printf("Starting audio streamer for stream: %s", as.streamName)

	// Connect to WhisperLiveKit WebSocket
	if err := as.connectToWhisper(); err != nil {
		return fmt.Errorf("failed to connect to WhisperLiveKit: %v", err)
	}

	// Start FFmpeg process to extract audio
	if err := as.startFFmpeg(); err != nil {
		return fmt.Errorf("failed to start FFmpeg: %v", err)
	}

	// Start goroutines for processing
	go as.readAudioFromFFmpeg()
	go as.sendAudioToWhisper()
	go as.receiveTranscriptions()

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

func (as *AudioStreamer) readAudioFromFFmpeg() {
	defer close(as.audioBuffer)

	buffer := make([]byte, as.chunkSize*2) // 2 bytes per sample for 16-bit

	for {
		select {
		case <-as.ctx.Done():
			return
		default:
			n, err := as.ffmpegStdout.Read(buffer)
			if err != nil {
				if err != io.EOF {
					log.Printf("Error reading from FFmpeg: %v", err)
				}
				return
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

			// Send raw audio bytes to WhisperLiveKit
			if err := as.conn.WriteMessage(websocket.BinaryMessage, audioChunk); err != nil {
				log.Printf("Error sending audio to WhisperLiveKit: %v", err)
				return
			}
		}
	}
}

func (as *AudioStreamer) receiveTranscriptions() {
	for {
		select {
		case <-as.ctx.Done():
			return
		default:
			_, message, err := as.conn.ReadMessage()
			if err != nil {
				log.Printf("Error reading from WhisperLiveKit: %v", err)
				return
			}

			// Process the transcription response
			transcript := string(message)
			as.processor.ProcessTranscript(transcript, as.streamName)
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
