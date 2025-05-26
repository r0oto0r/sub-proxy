package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WhisperResponse represents the JSON response from WhisperLiveKit
type WhisperResponse struct {
	Type                       string  `json:"type,omitempty"`
	Lines                      []Line  `json:"lines,omitempty"`
	BufferTranscription        string  `json:"buffer_transcription,omitempty"`
	BufferDiarization          string  `json:"buffer_diarization,omitempty"`
	RemainingTimeTranscription float64 `json:"remaining_time_transcription,omitempty"`
	RemainingTimeDiarization   float64 `json:"remaining_time_diarization,omitempty"`
	Status                     string  `json:"status,omitempty"`  // For status messages like "ready_to_stop"
	Message                    string  `json:"message,omitempty"` // For error messages or other info
	Error                      string  `json:"error,omitempty"`   // For error responses
}

// Line represents a transcription line with speaker and timing info
type Line struct {
	Speaker int    `json:"speaker"`
	Text    string `json:"text"`
	Beg     string `json:"beg,omitempty"`
	End     string `json:"end,omitempty"`
}

// YouTubeCaptionConfig holds configuration for YouTube caption streaming
type YouTubeCaptionConfig struct {
	CID               string
	Enabled           bool
	ServerDeltaMs     int64
	SequenceNumber    int
	StreamDelayMs     int64 // 10 second delay for stream start
	LastSentTimestamp time.Time
	mu                sync.Mutex
}

// TranscriptProcessor handles transcription responses from WhisperLiveKit
type TranscriptProcessor struct {
	mu             sync.RWMutex
	transcripts    map[string][]WhisperResponse     // streamName -> list of transcription responses
	youtubeConfigs map[string]*YouTubeCaptionConfig // streamName -> YouTube caption config
}

func NewTranscriptProcessor() *TranscriptProcessor {
	return &TranscriptProcessor{
		transcripts:    make(map[string][]WhisperResponse),
		youtubeConfigs: make(map[string]*YouTubeCaptionConfig),
	}
}

func (tp *TranscriptProcessor) ProcessTranscript(transcript string, streamName string) {
	// Recover from any panics to prevent the service from crashing
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in ProcessTranscript: %v", r)
		}
	}()

	tp.mu.Lock()
	defer tp.mu.Unlock()

	// Validate input
	if transcript == "" {
		log.Printf("[%s] Received empty transcript, skipping", streamName)
		return
	}

	// Log raw WebSocket message for debugging
	log.Printf("[%s] Raw WebSocket message: %s", streamName, transcript)

	// Try to parse as WhisperLiveKit JSON response
	var whisperResp WhisperResponse
	if err := json.Unmarshal([]byte(transcript), &whisperResp); err != nil {
		log.Printf("[%s] Failed to parse as JSON, treating as plain text: %v", streamName, err)
		// Create a simple response for plain text
		whisperResp = WhisperResponse{
			Type: "transcription",
			Lines: []Line{{
				Speaker: 0,
				Text:    transcript,
			}},
		}
	} else {
		// Handle different WhisperLiveKit message types
		switch whisperResp.Type {
		case "ready_to_stop", "status":
			log.Printf("[%s] WhisperLiveKit status: %s %s", streamName, whisperResp.Type, whisperResp.Status)
			if whisperResp.Message != "" {
				log.Printf("[%s] Status message: %s", streamName, whisperResp.Message)
			}
			// Don't store status messages as transcripts
			return
		case "error":
			log.Printf("[%s] WhisperLiveKit error: %s", streamName, whisperResp.Error)
			if whisperResp.Message != "" {
				log.Printf("[%s] Error details: %s", streamName, whisperResp.Message)
			}
			// Don't store error messages as transcripts
			return
		case "transcription", "partial", "final", "":
			// These are actual transcription messages, continue processing
			break
		default:
			log.Printf("[%s] Unknown message type from WhisperLiveKit: %s", streamName, whisperResp.Type)
		}
	}

	// Store transcript in memory only if it contains meaningful content
	if tp.hasTranscriptionContent(whisperResp) {
		if tp.transcripts[streamName] == nil {
			tp.transcripts[streamName] = make([]WhisperResponse, 0)
		}
		tp.transcripts[streamName] = append(tp.transcripts[streamName], whisperResp)

		// Keep only the last 100 transcripts to prevent memory growth
		if len(tp.transcripts[streamName]) > 100 {
			tp.transcripts[streamName] = tp.transcripts[streamName][len(tp.transcripts[streamName])-100:]
		}
	}

	// Log processed transcript content and send to YouTube if configured
	if len(whisperResp.Lines) > 0 {
		for _, line := range whisperResp.Lines {
			if line.Text != "" {
				log.Printf("[%s] Speaker %d: %s", streamName, line.Speaker, line.Text)

				// Send to YouTube captions if configured
				if config, exists := tp.youtubeConfigs[streamName]; exists && config.Enabled {
					go func(text string) {
						if err := tp.postCaptionAndSync(text, streamName); err != nil {
							log.Printf("[%s] Failed to send caption to YouTube: %v", streamName, err)
						}
					}(line.Text)
				}
			}
		}
	}
	if whisperResp.BufferTranscription != "" {
		log.Printf("[%s] Buffer: %s", streamName, whisperResp.BufferTranscription)

		// Send buffer transcription to YouTube if configured and no lines were sent
		if len(whisperResp.Lines) == 0 {
			if config, exists := tp.youtubeConfigs[streamName]; exists && config.Enabled {
				go func(text string) {
					if err := tp.postCaptionAndSync(text, streamName); err != nil {
						log.Printf("[%s] Failed to send buffer caption to YouTube: %v", streamName, err)
					}
				}(whisperResp.BufferTranscription)
			}
		}
	}

	// TODO: Implement actual transcript processing logic
	// This could include:
	// - Storing transcripts in database
	// - Sending to overlay system via WebSocket
	// - Broadcasting to connected clients
	// - Language processing/filtering
	// - Subtitle generation and overlay
	// - Real-time translation
	// - Sentiment analysis
	// - Keyword detection and alerts
}

func (tp *TranscriptProcessor) GetTranscripts(streamName string) []string {
	tp.mu.RLock()
	defer tp.mu.RUnlock()

	if transcripts, exists := tp.transcripts[streamName]; exists {
		// Convert WhisperResponse objects to text strings
		result := make([]string, 0, len(transcripts))
		for _, resp := range transcripts {
			// Extract text from Lines
			for _, line := range resp.Lines {
				if line.Text != "" {
					result = append(result, line.Text)
				}
			}
			// Also include buffer transcription if available
			if resp.BufferTranscription != "" {
				result = append(result, resp.BufferTranscription)
			}
		}
		return result
	}
	return []string{}
}

// GetWhisperResponses returns the raw WhisperResponse objects for advanced processing
func (tp *TranscriptProcessor) GetWhisperResponses(streamName string) []WhisperResponse {
	tp.mu.RLock()
	defer tp.mu.RUnlock()

	if transcripts, exists := tp.transcripts[streamName]; exists {
		// Return a copy to avoid race conditions
		result := make([]WhisperResponse, len(transcripts))
		copy(result, transcripts)
		return result
	}
	return []WhisperResponse{}
}

// hasTranscriptionContent checks if a WhisperResponse contains meaningful transcription data
func (tp *TranscriptProcessor) hasTranscriptionContent(resp WhisperResponse) bool {
	// Check if there are lines with actual text
	for _, line := range resp.Lines {
		if line.Text != "" {
			return true
		}
	}

	// Check if there's buffer transcription
	if resp.BufferTranscription != "" {
		return true
	}

	// If it's a status, error, or empty message, don't store
	return false
}

func (tp *TranscriptProcessor) ClearTranscripts(streamName string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	delete(tp.transcripts, streamName)
	delete(tp.youtubeConfigs, streamName)
	log.Printf("Cleared transcripts and YouTube config for stream: %s", streamName)
}

// ConfigureYouTubeCaptions sets up YouTube caption streaming for a stream
func (tp *TranscriptProcessor) ConfigureYouTubeCaptions(streamName, cid string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	tp.youtubeConfigs[streamName] = &YouTubeCaptionConfig{
		CID:               cid,
		Enabled:           true,
		ServerDeltaMs:     0,
		SequenceNumber:    1,
		StreamDelayMs:     10000, // 10 second stream delay
		LastSentTimestamp: time.Now(),
	}

	log.Printf("[%s] YouTube captions configured with CID: %s", streamName, cid)
}

// getLocalIsoTimestamp formats current local time as ISO 8601 with milliseconds, adjusted for server delta and stream delay
func (tp *TranscriptProcessor) getLocalIsoTimestamp(streamName string) string {
	config := tp.youtubeConfigs[streamName]
	if config == nil {
		return time.Now().Format(time.RFC3339Nano)
	}

	// Adjust for server delta and stream delay
	adjustedTime := time.Now().Add(time.Duration(config.ServerDeltaMs)*time.Millisecond + time.Duration(config.StreamDelayMs)*time.Millisecond)
	return adjustedTime.Format(time.RFC3339Nano)
}

// parseIsoTimestamp parses ISO 8601 timestamp with milliseconds to time.Time
func parseIsoTimestamp(ts string) (time.Time, error) {
	// Try RFC3339Nano first (with nanoseconds)
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t, nil
	}
	// Fallback to RFC3339 (with seconds)
	return time.Parse(time.RFC3339, ts)
}

// postCaptionAndSync sends a caption to YouTube and syncs local clock with YouTube's response
func (tp *TranscriptProcessor) postCaptionAndSync(text, streamName string) error {
	config := tp.youtubeConfigs[streamName]
	if config == nil || !config.Enabled {
		return fmt.Errorf("YouTube captions not configured for stream %s", streamName)
	}

	config.mu.Lock()
	defer config.mu.Unlock()

	// Prevent sending captions too frequently (max once per second)
	if time.Since(config.LastSentTimestamp) < time.Second {
		return nil // Skip this caption
	}

	localTimestamp := tp.getLocalIsoTimestamp(streamName)
	body := fmt.Sprintf("%s\n%s\n", localTimestamp, text)

	// Build URL with parameters
	u, err := url.Parse("http://upload.youtube.com/closedcaption")
	if err != nil {
		return fmt.Errorf("failed to parse YouTube URL: %v", err)
	}

	q := u.Query()
	q.Set("cid", config.CID)
	q.Set("seq", strconv.Itoa(config.SequenceNumber))
	q.Set("lang", "en-US")
	u.RawQuery = q.Encode()

	// Create HTTP request with UTF-8 encoded body
	bodyBytes := []byte(body) // Explicitly convert to UTF-8 bytes
	req, err := http.NewRequest("POST", u.String(), bytes.NewBuffer(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	// Send request
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send caption: %v", err)
	}
	defer resp.Body.Close()

	// Check HTTP status code
	if resp.StatusCode != http.StatusOK {
		// Read response body for error details
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("YouTube API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Read server response
	serverResponseRaw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %v", err)
	}

	serverResponse := strings.TrimSpace(string(serverResponseRaw))

	// Check if response contains an error message (more than just a timestamp)
	// Split the response to separate timestamp from potential error message
	responseParts := strings.SplitN(serverResponse, " ", 2)
	serverTimestamp := responseParts[0]

	// If there's more than just a timestamp, it's an error
	if len(responseParts) > 1 {
		errorMessage := responseParts[1]
		log.Printf("[%s] YouTube caption error: %s", streamName, errorMessage)
		// Don't update sequence number or delta on error, but don't kill processing
		return fmt.Errorf("YouTube caption API error: %s", errorMessage)
	}

	// Calculate delta between local clock and YouTube's clock
	localSent, err := parseIsoTimestamp(localTimestamp)
	if err != nil {
		log.Printf("[%s] Error parsing local timestamp: %v", streamName, err)
		return fmt.Errorf("invalid local timestamp format: %v", err)
	}

	serverTime, err := parseIsoTimestamp(serverTimestamp)
	if err != nil {
		log.Printf("[%s] Error parsing server timestamp '%s': %v", streamName, serverTimestamp, err)
		return fmt.Errorf("invalid server timestamp format: %v", err)
	}

	config.ServerDeltaMs = serverTime.UnixMilli() - localSent.UnixMilli()
	config.SequenceNumber++
	config.LastSentTimestamp = time.Now()

	log.Printf("[%s] Caption sent - Local: %s, Server: %s, Delta: %dms, Seq: %d",
		streamName, localTimestamp, serverTimestamp, config.ServerDeltaMs, config.SequenceNumber-1)

	return nil
}
