package main

import (
	"encoding/json"
	"log"
	"sync"
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

// TranscriptProcessor handles transcription responses from WhisperLiveKit
type TranscriptProcessor struct {
	mu          sync.RWMutex
	transcripts map[string][]WhisperResponse // streamName -> list of transcription responses
}

func NewTranscriptProcessor() *TranscriptProcessor {
	return &TranscriptProcessor{
		transcripts: make(map[string][]WhisperResponse),
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

	// Log processed transcript content
	if len(whisperResp.Lines) > 0 {
		for _, line := range whisperResp.Lines {
			if line.Text != "" {
				log.Printf("[%s] Speaker %d: %s", streamName, line.Speaker, line.Text)
			}
		}
	}
	if whisperResp.BufferTranscription != "" {
		log.Printf("[%s] Buffer: %s", streamName, whisperResp.BufferTranscription)
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
	log.Printf("Cleared transcripts for stream: %s", streamName)
}
