package main

import (
	"log"
	"sync"
)

// TranscriptProcessor handles transcription responses (stub for now)
type TranscriptProcessor struct {
	mu          sync.RWMutex
	transcripts map[string][]string // streamName -> list of transcripts
}

func NewTranscriptProcessor() *TranscriptProcessor {
	return &TranscriptProcessor{
		transcripts: make(map[string][]string),
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

	// Stub implementation - just log for now
	log.Printf("[%s] Transcript: %s", streamName, transcript)

	// Store transcript in memory (for demo purposes)
	if tp.transcripts[streamName] == nil {
		tp.transcripts[streamName] = make([]string, 0)
	}
	tp.transcripts[streamName] = append(tp.transcripts[streamName], transcript)

	// Keep only the last 100 transcripts to prevent memory growth
	if len(tp.transcripts[streamName]) > 100 {
		tp.transcripts[streamName] = tp.transcripts[streamName][len(tp.transcripts[streamName])-100:]
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
		// Return a copy to avoid race conditions
		result := make([]string, len(transcripts))
		copy(result, transcripts)
		return result
	}
	return []string{}
}

func (tp *TranscriptProcessor) ClearTranscripts(streamName string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	delete(tp.transcripts, streamName)
	log.Printf("Cleared transcripts for stream: %s", streamName)
}
