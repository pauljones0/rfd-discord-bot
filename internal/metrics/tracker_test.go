package metrics

import "testing"

func TestTracker_BasicOperations(t *testing.T) {
	tracker := NewTracker("test")

	tracker.TrackGeminiCall(100, 50)
	tracker.TrackGeminiCall(200, 75)
	tracker.TrackCarfaxValuation(true)
	tracker.TrackCarfaxValuation(true)
	tracker.TrackCarfaxValuation(false)
	tracker.TrackDiscordMessage()
	tracker.TrackAdsScraped(25)
	tracker.TrackAdProcessed()
	tracker.TrackAdProcessed()
	tracker.TrackDealFound()
	tracker.TrackAIOutcome("title_cleaning", 10, 8, 2, 1, 3)
	tracker.TrackAIDecision("bestbuy", true)
	tracker.TrackAIDecision("bestbuy", false)

	if tracker.geminiCalls.Load() != 2 {
		t.Errorf("expected 2 gemini calls, got %d", tracker.geminiCalls.Load())
	}
	if tracker.geminiInputTokens.Load() != 300 {
		t.Errorf("expected 300 input tokens, got %d", tracker.geminiInputTokens.Load())
	}
	if tracker.geminiOutputTokens.Load() != 125 {
		t.Errorf("expected 125 output tokens, got %d", tracker.geminiOutputTokens.Load())
	}
	if tracker.carfaxValuations.Load() != 2 {
		t.Errorf("expected 2 carfax valuations, got %d", tracker.carfaxValuations.Load())
	}
	if tracker.carfaxFailures.Load() != 1 {
		t.Errorf("expected 1 carfax failure, got %d", tracker.carfaxFailures.Load())
	}
	if tracker.discordMessagesSent.Load() != 1 {
		t.Errorf("expected 1 discord message, got %d", tracker.discordMessagesSent.Load())
	}
	if tracker.adsScraped.Load() != 25 {
		t.Errorf("expected 25 ads scraped, got %d", tracker.adsScraped.Load())
	}
	if tracker.adsProcessed.Load() != 2 {
		t.Errorf("expected 2 ads processed, got %d", tracker.adsProcessed.Load())
	}
	if tracker.dealsFound.Load() != 1 {
		t.Errorf("expected 1 deal found, got %d", tracker.dealsFound.Load())
	}
	if tracker.aiRequests.Load() != 10 || tracker.aiReturned.Load() != 8 || tracker.aiMissing.Load() != 2 {
		t.Errorf("unexpected ai outcome counters: requested=%d returned=%d missing=%d", tracker.aiRequests.Load(), tracker.aiReturned.Load(), tracker.aiMissing.Load())
	}
	if tracker.aiParseFailures.Load() != 1 || tracker.aiRetries.Load() != 3 {
		t.Errorf("unexpected ai failure counters: parse=%d retries=%d", tracker.aiParseFailures.Load(), tracker.aiRetries.Load())
	}
	if tracker.aiPosted.Load() != 1 || tracker.aiNotPosted.Load() != 1 {
		t.Errorf("unexpected ai decision counters: posted=%d not_posted=%d", tracker.aiPosted.Load(), tracker.aiNotPosted.Load())
	}

	// Just verify LogSummary doesn't panic
	tracker.LogSummary()
}

func TestTracker_ThreadSafety(t *testing.T) {
	tracker := NewTracker("concurrent-test")

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				tracker.TrackGeminiCall(10, 5)
				tracker.TrackCarfaxValuation(true)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	if tracker.geminiCalls.Load() != 1000 {
		t.Errorf("expected 1000 gemini calls, got %d", tracker.geminiCalls.Load())
	}
}
