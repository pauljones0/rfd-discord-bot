package metrics

import "testing"

func TestTracker_BasicOperations(t *testing.T) {
	tracker := NewTracker("test")

	tracker.TrackGeminiCall("gemini-2.5-flash", "us-central1", 100, 50)
	tracker.TrackGeminiCall("gemini-2.5-flash", "us-central1", 200, 75)
	tracker.TrackCarfaxValuation(true)
	tracker.TrackCarfaxValuation(true)
	tracker.TrackCarfaxValuation(false)
	tracker.TrackDiscordMessage()
	tracker.TrackAdsScraped(25)
	tracker.TrackAdProcessed()
	tracker.TrackAdProcessed()
	tracker.TrackDealFound()

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

	// Just verify LogSummary doesn't panic
	tracker.LogSummary()
}

func TestTracker_ThreadSafety(t *testing.T) {
	tracker := NewTracker("concurrent-test")

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				tracker.TrackGeminiCall("model", "region", 10, 5)
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
