package models

import (
	"testing"
)

func TestDealInfo_Stats(t *testing.T) {
	tests := []struct {
		name         string
		threads      []ThreadContext
		wantLikes    int
		wantComments int
		wantViews    int
	}{
		{
			name:         "Empty threads",
			threads:      nil,
			wantLikes:    0,
			wantComments: 0,
			wantViews:    0,
		},
		{
			name: "Single thread with zero values",
			threads: []ThreadContext{
				{LikeCount: 0, CommentCount: 0, ViewCount: 0},
			},
			wantLikes:    0,
			wantComments: 0,
			wantViews:    0,
		},
		{
			name: "Single thread with positive values",
			threads: []ThreadContext{
				{LikeCount: 10, CommentCount: 5, ViewCount: 100},
			},
			wantLikes:    10,
			wantComments: 5,
			wantViews:    100,
		},
		{
			name: "Single thread with negative likes",
			threads: []ThreadContext{
				{LikeCount: -5, CommentCount: 2, ViewCount: 50},
			},
			wantLikes:    -5,
			wantComments: 2,
			wantViews:    50,
		},
		{
			name: "Multiple threads averaging perfectly",
			threads: []ThreadContext{
				{LikeCount: 10, CommentCount: 5, ViewCount: 100},
				{LikeCount: 20, CommentCount: 15, ViewCount: 300},
			},
			wantLikes:    15,
			wantComments: 10,
			wantViews:    200,
		},
		{
			name: "Multiple threads with rounding towards zero",
			threads: []ThreadContext{
				{LikeCount: 10, CommentCount: 5, ViewCount: 100},
				{LikeCount: 11, CommentCount: 6, ViewCount: 101},
			},
			// 21 / 2 = 10, 11 / 2 = 5, 201 / 2 = 100
			wantLikes:    10,
			wantComments: 5,
			wantViews:    100,
		},
		{
			name: "Multiple threads with negative likes rounding towards zero",
			threads: []ThreadContext{
				{LikeCount: -5, CommentCount: 2, ViewCount: 50},
				{LikeCount: -2, CommentCount: 4, ViewCount: 51},
			},
			// -7 / 2 = -3 in Go
			wantLikes:    -3,
			wantComments: 3,
			wantViews:    50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deal := &DealInfo{
				Threads: tt.threads,
			}
			gotLikes, gotComments, gotViews := deal.Stats()
			if gotLikes != tt.wantLikes {
				t.Errorf("DealInfo.Stats() gotLikes = %v, want %v", gotLikes, tt.wantLikes)
			}
			if gotComments != tt.wantComments {
				t.Errorf("DealInfo.Stats() gotComments = %v, want %v", gotComments, tt.wantComments)
			}
			if gotViews != tt.wantViews {
				t.Errorf("DealInfo.Stats() gotViews = %v, want %v", gotViews, tt.wantViews)
			}
		})
	}
}

func TestDealInfo_PrimaryPostURL(t *testing.T) {
	tests := []struct {
		name    string
		postURL string
		threads []ThreadContext
		want    string
	}{
		{
			name:    "Empty threads falls back to PostURL",
			postURL: "https://example.com/legacy",
			threads: nil,
			want:    "https://example.com/legacy",
		},
		{
			name:    "Single thread returns its PostURL",
			postURL: "https://example.com/legacy",
			threads: []ThreadContext{
				{PostURL: "https://example.com/thread1"},
			},
			want: "https://example.com/thread1",
		},
		{
			name:    "Multiple threads returns first thread PostURL",
			postURL: "https://example.com/legacy",
			threads: []ThreadContext{
				{PostURL: "https://example.com/thread1"},
				{PostURL: "https://example.com/thread2"},
			},
			want: "https://example.com/thread1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deal := &DealInfo{
				PostURL: tt.postURL,
				Threads: tt.threads,
			}
			if got := deal.PrimaryPostURL(); got != tt.want {
				t.Errorf("DealInfo.PrimaryPostURL() = %v, want %v", got, tt.want)
			}
		})
	}
}
