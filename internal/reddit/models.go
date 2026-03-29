package reddit

// Feed represents the top-level Reddit .json response structure.
type Feed struct {
	Data struct {
		Children []struct {
			Data Post `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

// Post is a single Reddit post from the .json feed.
type Post struct {
	ID                string  `json:"id"`
	Title             string  `json:"title"`
	SelfText          string  `json:"selftext"`
	URL               string  `json:"url"`
	Permalink         string  `json:"permalink"`
	Subreddit         string  `json:"subreddit"`
	CreatedUtc        float64 `json:"created_utc"`
	Author            string  `json:"author"`
	Score             int     `json:"score"`
	NumComments       int     `json:"num_comments"`
	LinkFlairText     string  `json:"link_flair_text"`
	RemovedByCategory string  `json:"removed_by_category"`
	Thumbnail         string  `json:"thumbnail"`
}
