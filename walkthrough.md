# Walkthrough: Smart Deal Deduplication

## What was Accomplished

We successfully implemented a robust **Smart Deal Deduplication** feature for the RFD Discord bot. The objective was to eliminate duplicate notifications when users submit the identical deal multiple times (a common occurrence on RedFlagDeals), without sacrificing any important metric aggregations or link availability.

### Architecture Shifts

1. **[ThreadContext](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/models/deal.go#49-56) Array vs 1:1:** The primary [DealInfo](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/models/deal.go#12-47) model was successfully migrated from a flat 1:1 structure (where 1 deal = 1 forum thread) to 1-to-many. The [DealInfo](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/models/deal.go#12-47) now owns entirely an array of `Threads []ThreadContext`, merging engagement metrics effectively into a single parent document containing individual context on its component threads.
2. **Search Tokens:** URLs and Titles undergo tokenization on scrape. Tokens are collected into a **deduplicated set** to prevent inflation of similarity scores from repeated terms (e.g. `"$40/mo + $40 credit"` produces one `"40"`, not two). Non-valuable tokens are aggressively filtered via an expanded package-level stopword list (~32 words including `"best"`, `"price"`, `"free"`, `"offer"`, `"drop"`, etc.). URL tokens additionally strip TLD/protocol noise (`"www"`, `"com"`, `"ca"`, `"html"`) while preserving valuable retailer names like `"amazon"`.
3. **Cross-Batch Deduplication:** By leveraging a dynamic [GetRecentDeals](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/processor/processor_test.go#71-78) function retrieving Firestore documents dating back up to 48 hours, new scrapes aren't just compared to themselves within a small isolated batch. They effectively reach backwards in time and associate seamlessly with pre-existing parent deals if titles or tokenized URLs fuzzy match at a tight `>= 80%` overlap coefficient threshold. Intra-batch merging now correctly handles **3+ duplicates** in a single scrape without breaking after the first match.

### Resulting Output
- **Consolidated Updates:** Instead of spamming multiple different messages into Discord subscribed channels, a scraped duplicate simply mutates and "piggybacks" off the original.
- **Dynamic Popularity Ranking:** The resulting Discord embed uses the parent struct getters `LikeCount`, `CommentCount`, etc., which now properly return mathematically aggregated metrics among all valid duplicates. The `[RFD]` URL fields dynamically swap position with whatever thread holds the most likes internally at generation.

## Test Validation
Extensive tests were added encompassing Token Generation [TestGenerateSearchTokens](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/processor/dedupe_test.go#27-67), duplicate prevention [TestGenerateSearchTokens_NoDuplicates](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/processor/dedupe_test.go#176-206), URL noise filtering [TestGenerateSearchTokens_URLDomainNoise](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/processor/dedupe_test.go#208-243), similarity comparisons in [TestCalculateSimilarity](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/processor/dedupe_test.go#69-93), and the grouping mechanisms including [TestDeduplicateDeals_ThreeWayMerge](file:///c:/Users/bethe/Downloads/rfd-discord-bot/internal/processor/dedupe_test.go#245-275).

As verified:

```
ok  	github.com/pauljones0/rfd-discord-bot/internal/processor	1.363s
ok  	github.com/pauljones0/rfd-discord-bot/internal/config	(cached)
ok  	github.com/pauljones0/rfd-discord-bot/internal/notifier	(cached)
ok  	github.com/pauljones0/rfd-discord-bot/internal/util	(cached)
ok  	github.com/pauljones0/rfd-discord-bot/internal/validator	(cached)
```

No more annoying spam notifications for the identical `Galaxy S24` deals!
