use crate::time::{Timestamp, null_default};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct DealInfo {
    #[serde(rename = "Title")]
    pub title: String,
    #[serde(rename = "PostURL")]
    pub post_url: String,
    #[serde(rename = "Category")]
    pub category: String,
    #[serde(rename = "ThreadImageURL")]
    pub thread_image_url: String,
    #[serde(rename = "ActualDealURL")]
    pub actual_deal_url: String,
    #[serde(rename = "DocumentID")]
    pub document_id: String,
    #[serde(rename = "DiscordMessageIDs", deserialize_with = "null_default")]
    pub discord_message_ids: BTreeMap<String, String>,
    #[serde(
        rename = "DiscordMessageApplicationIDs",
        deserialize_with = "null_default"
    )]
    pub discord_message_application_ids: BTreeMap<String, String>,
    #[serde(rename = "LastUpdated")]
    pub last_updated: Timestamp,
    #[serde(rename = "PublishedTimestamp")]
    pub published_timestamp: Timestamp,
    #[serde(rename = "DiscordLastUpdatedTime")]
    pub discord_last_updated_time: Timestamp,
    #[serde(rename = "ExpiresAt")]
    pub expires_at: Timestamp,
    #[serde(rename = "Threads", deserialize_with = "null_default")]
    pub threads: Vec<ThreadContext>,
    #[serde(rename = "SearchTokens", deserialize_with = "null_default")]
    pub search_tokens: Vec<String>,
    #[serde(rename = "Price")]
    pub price: String,
    #[serde(rename = "OriginalPrice")]
    pub original_price: String,
    #[serde(rename = "Savings")]
    pub savings: String,
    #[serde(rename = "Retailer")]
    pub retailer: String,
    #[serde(rename = "CleanTitle")]
    pub clean_title: String,
    #[serde(rename = "AIProcessed")]
    pub ai_processed: bool,
    #[serde(rename = "HasBeenWarm")]
    pub has_been_warm: bool,
    #[serde(rename = "HasBeenHot")]
    pub has_been_hot: bool,
    #[serde(rename = "Description")]
    pub description: String,
    #[serde(rename = "Comments")]
    pub comments: String,
    #[serde(rename = "Summary")]
    pub summary: String,
}

#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ThreadContext {
    #[serde(rename = "DocumentID")]
    pub document_id: String,
    #[serde(rename = "PostURL")]
    pub post_url: String,
    #[serde(rename = "LikeCount")]
    pub like_count: i64,
    #[serde(rename = "CommentCount")]
    pub comment_count: i64,
    #[serde(rename = "ViewCount")]
    pub view_count: i64,
    #[serde(rename = "ViewCountAvailable")]
    pub view_count_available: bool,
    #[serde(rename = "NotFound")]
    pub not_found: bool,
}

#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Subscription {
    #[serde(rename = "GuildID")]
    pub guild_id: String,
    #[serde(rename = "ChannelID")]
    pub channel_id: String,
    #[serde(rename = "ChannelName")]
    pub channel_name: String,
    #[serde(rename = "DealType")]
    pub deal_type: String,
    #[serde(rename = "AddedBy")]
    pub added_by: String,
    #[serde(rename = "AddedAt")]
    pub added_at: Timestamp,
    #[serde(rename = "SubscriptionType")]
    pub subscription_type: String,
}

#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct GeminiQuotaStatus {
    #[serde(rename = "DailyRequests")]
    pub daily_requests: u32,
    #[serde(rename = "LastRequestAt")]
    pub last_request_at: Timestamp,
    #[serde(rename = "CurrentDay")]
    pub current_day: String,
    #[serde(rename = "CurrentModel")]
    pub current_model: String,
    #[serde(rename = "AllExhausted")]
    pub all_exhausted: bool,
    #[serde(rename = "ExhaustedAt")]
    pub exhausted_at: Timestamp,
    #[serde(rename = "CurrentLocation")]
    pub current_location: String,
    #[serde(rename = "LastUpdated")]
    pub last_updated: Timestamp,
}
