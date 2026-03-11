package processor

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func BenchmarkSortThreads(b *testing.B) {
	sizes := []int{10, 50, 100, 500}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("Size_%d", size), func(b *testing.B) {
			deal := generateDeal(size)
			p := &DealProcessor{} // sortThreads has no side effects on processor state
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Make a copy to avoid sorting an already sorted slice
				clone := &models.DealInfo{Threads: make([]models.ThreadContext, len(deal.Threads))}
				copy(clone.Threads, deal.Threads)
				p.sortThreads(clone)
			}
		})
	}
}

func generateDeal(numThreads int) *models.DealInfo {
	deal := &models.DealInfo{}
	for i := 0; i < numThreads; i++ {
		deal.Threads = append(deal.Threads, models.ThreadContext{
			LikeCount:    rand.Intn(100),
			CommentCount: rand.Intn(50),
		})
	}
	return deal
}
