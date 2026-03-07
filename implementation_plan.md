# Deal Deduplication Implementation Plan

## Problem Description
Users post identical deals in separate threads, causing multiple Discord alerts. We need a robust deduplication mechanism that merges identical deals securely. The user raised excellent points: URL tokenization should be included, link popularities can change over time requiring re-ordering, and duplicate deals might not appear in the same scraping batch.

## Proposed Architecture: The `ThreadContext` Model
Instead of treating [DealInfo](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/models/deal.go#12-47) as a 1:1 mapping to a single RedFlagDeals thread, we elevate [DealInfo](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/models/deal.go#12-47) to represent the **Abstract Deal Idea**, which can contain multiple **Threads**.

### 1. [internal/models/deal.go](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/models/deal.go)
#### [MODIFY] [deal.go](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/models/deal.go)
- Add a new struct:
  ```go
  type ThreadContext struct {
      FirestoreID  string `firestore:"firestoreID"`
      PostURL      string `firestore:"postURL"`
      LikeCount    int    `firestore:"likeCount"`
      CommentCount int    `firestore:"commentCount"`
      ViewCount    int    `firestore:"viewCount"`
  }
  ```
- Modify [DealInfo](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/models/deal.go#12-47):
  - Replace `PostURL`, `LikeCount`, `CommentCount`, `ViewCount` with `Threads []ThreadContext`.
  - The getters for those stats will become aggregate functions:
    - Primary PostURL = `Threads[0].PostURL`
    - Aggregate Likes = `Round(Sum(Threads.Likes) / len(Threads))`
  - Add `SearchTokens []string` to store tokenized words/numbers.

### 2. [internal/processor/processor.go](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/processor/processor.go)
#### [MODIFY] [processor.go](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/processor/processor.go)
- **Tokenization**:
  - `CleanTitle` tokens: Lowercase, remove punctuation, extract numbers/units/brands.
  - `ActualDealURL` tokens: Extract the slug, split by hyphens/slashes, and append high-value English words/brands to the token list.
- **Cross-Batch Deduplication Flow**:
  1. **Scrape**: We scrape a batch of deals.
  2. **Convert**: Convert them into [DealInfo](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/models/deal.go#12-47) objects where `Threads` has exactly 1 entry.
  3. **Fetch Existing**: Instead of just doing a direct Firestore ID lookup, we query Firestore for recent deals (e.g., last 48 hours).
  4. **Fuzzy Match Engine**: For each scraped deal:
     - Check if it matches an *existing* Firestore deal (via exact `ActualDealURL` or high fuzzy match score on `SearchTokens`).
     - Check if it matches *another deal in the current scrape batch*.
  5. **Merge**: If Deal A (scraped) matches Deal B (existing or also scraped):
     - We don't save Deal A as a new document.
     - We push Deal A's thread stats into Deal B's `Threads` array (or update it if that specific thread was already in the array).
     - We map Deal A's FirestoreID pointer to Deal B's FirestoreID so any updates go to the parent entity.
  6. **Re-ordering & Aggregation**: Before saving or sending to Discord, we sort `Threads` by `LikeCount` descending. This ensures the most popular thread is always `Threads[0]`.

### 3. [internal/storage/firestore.go](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/storage/firestore.go)
#### [MODIFY] [firestore.go](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/storage/firestore.go)
- Add `GetRecentDeals(ctx, duration)` to fetch deals from the last 24-48 hours. This is required because we can't just look up duplicates by ID anymore; we must bring recent deals into memory to run the fuzzy matcher against them.
- Update [UpdateDeal](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/storage/firestore.go#133-185) to save the `Threads` array instead of individual stats.

### 4. [internal/notifier/discord.go](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/notifier/discord.go)
#### [MODIFY] `discord.go`
- Update embed logic to use `deal.Threads`.
- Display logic:
  - Loop through `deal.Threads`.
  - For `Threads[0]`, hyperlinked text is `[RFD]`.
  - For `Threads[1:]`, append ` [RFD]` right next to it.
  - Because `Threads` inherently sorts by `LikeCount` in the processor, the links will automatically re-order themselves in the embed if a newer thread suddenly becomes more popular!

### 5. [internal/processor/processor_test.go](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/processor/processor_test.go)
#### [MODIFY] [processor_test.go](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/processor/processor_test.go)
- Add unit tests for the tokenization (including URL slug tokenization).
- Add tests to ensure that a newly scraped deal successfully merges into an "older" existing deal context, updates the `Threads` array, recalculates stats, and resorts the array.

## Verification Plan
### Automated Tests
- Run `go test ./internal/...` focusing on testing `ThreadContext` merging and sorting.

### Manual Verification
- Seed Firestore with Deal A.
- Scrape Deal B (a duplicate).
- Verify no new Discord message is sent, but Deal A's Discord message is updated to contain both Deal A's and Deal B's links, sorted by whichever has more likes.
