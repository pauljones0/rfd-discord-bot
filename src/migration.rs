use crate::{
    models::{DealInfo, Subscription},
    storage::{APPLICATION_KEY, Store, valid_discord_id, valid_subscription, validate_schema},
    time::Timestamp,
};
use anyhow::{Result, ensure};
use rusqlite::{OptionalExtension, params};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::{BTreeMap, BTreeSet};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Migration {
    pub version: i64,
    pub source_application_id: String,
    pub exported_at: Timestamp,
    pub subscriptions: Vec<Subscription>,
    pub deals: Vec<DealInfo>,
}
#[derive(Debug, Serialize)]
pub struct ImportResult {
    pub version: i64,
    pub source_application_id: String,
    pub target_application_id: String,
    pub exported_at: Timestamp,
    pub imported_at: Timestamp,
    pub subscriptions: usize,
    pub deals: usize,
    pub message_receipts: usize,
}
impl Migration {
    pub fn decode(raw: &[u8]) -> Result<Self> {
        ensure!(raw.len() <= 64 * 1024 * 1024, "migration exceeds 64 MiB");
        let value: Value = serde_json::from_slice(raw)?;
        let subscription_keys = [
            "GuildID",
            "ChannelID",
            "ChannelName",
            "DealType",
            "AddedBy",
            "AddedAt",
            "SubscriptionType",
        ];
        let deal_keys = [
            "Title",
            "PostURL",
            "Category",
            "ThreadImageURL",
            "ActualDealURL",
            "DocumentID",
            "DiscordMessageIDs",
            "DiscordMessageApplicationIDs",
            "LastUpdated",
            "PublishedTimestamp",
            "DiscordLastUpdatedTime",
            "ExpiresAt",
            "Threads",
            "SearchTokens",
            "Price",
            "OriginalPrice",
            "Savings",
            "Retailer",
            "CleanTitle",
            "AIProcessed",
            "HasBeenWarm",
            "HasBeenHot",
            "Description",
            "Comments",
            "Summary",
        ];
        let thread_keys = [
            "DocumentID",
            "PostURL",
            "LikeCount",
            "CommentCount",
            "ViewCount",
            "ViewCountAvailable",
            "NotFound",
        ];
        if let Some(subs) = value.get("subscriptions").and_then(Value::as_array) {
            for sub in subs {
                known_keys(sub, &subscription_keys)?;
            }
        }
        if let Some(deals) = value.get("deals").and_then(Value::as_array) {
            for d in deals {
                known_keys(d, &deal_keys)?;
                if let Some(threads) = d.get("Threads").and_then(Value::as_array) {
                    for t in threads {
                        known_keys(t, &thread_keys)?;
                    }
                }
            }
        }
        Ok(serde_json::from_value(value)?)
    }
    fn validate(&self, source: &str, target: &str) -> Result<()> {
        ensure!(self.version == 1, "migration version must be 1");
        ensure!(
            valid_discord_id(source) && valid_discord_id(target),
            "expected source/target must be Discord snowflakes"
        );
        ensure!(
            self.source_application_id == source,
            "migration source application does not match expected source"
        );
        valid_timestamp(&self.exported_at, true)?;
        ensure!(
            self.subscriptions.len() <= 10000 && self.deals.len() <= 100000,
            "migration exceeds supported record counts"
        );
        let mut seen = BTreeSet::new();
        let mut channel_guilds = BTreeMap::new();
        for s in &self.subscriptions {
            ensure!(
                valid_discord_id(&s.guild_id)
                    && valid_discord_id(&s.channel_id)
                    && valid_subscription(s),
                "invalid RFD subscription"
            );
            ensure!(
                !s.added_by.chars().any(char::is_control),
                "invalid attribution text"
            );
            valid_timestamp(&s.added_at, false)?;
            ensure!(
                seen.insert((&s.guild_id, &s.channel_id, &s.deal_type)),
                "duplicate subscription"
            );
            if let Some(old) = channel_guilds.insert(&s.channel_id, &s.guild_id) {
                ensure!(
                    old == &s.guild_id,
                    "one channel assigned to different guilds"
                );
            }
        }
        let mut seen = BTreeSet::new();
        for d in &self.deals {
            ensure!(
                valid_document_id(&d.document_id) && !d.title.trim().is_empty(),
                "deal requires valid ID and title"
            );
            ensure!(seen.insert(&d.document_id), "duplicate document ID");
            ensure!(
                valid_web_url(&d.post_url, true)
                    && valid_web_url(&d.actual_deal_url, false)
                    && valid_web_url(&d.thread_image_url, false),
                "invalid deal URL"
            );
            valid_timestamp(&d.published_timestamp, true)?;
            for t in [&d.last_updated, &d.discord_last_updated_time, &d.expires_at] {
                valid_timestamp(t, false)?;
            }
            for (c, m) in &d.discord_message_ids {
                ensure!(
                    valid_discord_id(c) && valid_discord_id(m),
                    "invalid message receipt"
                );
            }
            for (c, a) in &d.discord_message_application_ids {
                ensure!(
                    d.discord_message_ids.contains_key(c) && a == source,
                    "inconsistent message ownership"
                );
            }
            let mut seen = BTreeSet::new();
            for t in &d.threads {
                ensure!(
                    valid_document_id(&t.document_id)
                        && valid_web_url(&t.post_url, true)
                        && t.comment_count >= 0
                        && t.view_count >= 0,
                    "invalid thread"
                );
                ensure!(
                    seen.insert((&t.document_id, &t.post_url)),
                    "duplicate thread"
                );
            }
        }
        Ok(())
    }
}
impl Store {
    pub fn import(&mut self, m: &Migration, source: &str, target: &str) -> Result<ImportResult> {
        m.validate(source, target)?;
        let tx = self.db.transaction()?;
        validate_schema(&tx)?;
        let count:i64=tx.query_row("SELECT (SELECT COUNT(*) FROM deals)+(SELECT COUNT(*) FROM subscriptions)+(SELECT COUNT(*) FROM settings WHERE key<>?)",[APPLICATION_KEY],|r|r.get(0))?;
        ensure!(count == 0, "migration destination is not empty");
        let old: Option<String> = tx
            .query_row(
                "SELECT payload FROM settings WHERE key=?",
                [APPLICATION_KEY],
                |r| r.get(0),
            )
            .optional()?;
        ensure!(
            old.is_none_or(|v| v == target),
            "destination belongs to another application"
        );
        let mut result = ImportResult {
            version: 1,
            source_application_id: source.into(),
            target_application_id: target.into(),
            exported_at: m.exported_at.clone(),
            imported_at: Timestamp::now(),
            subscriptions: m.subscriptions.len(),
            deals: m.deals.len(),
            message_receipts: 0,
        };
        for s in &m.subscriptions {
            let mut s = s.clone();
            s.subscription_type = "rfd".into();
            tx.execute(
                "INSERT INTO subscriptions VALUES(?,?,?,?)",
                params![
                    s.guild_id,
                    s.channel_id,
                    s.deal_type,
                    serde_json::to_string(&s)?
                ],
            )?;
        }
        for d in &m.deals {
            let mut d = d.clone();
            d.discord_message_application_ids = d
                .discord_message_ids
                .keys()
                .map(|c| (c.clone(), source.into()))
                .collect();
            result.message_receipts += d.discord_message_ids.len();
            tx.execute(
                "INSERT INTO deals VALUES(?,?,?,?)",
                params![
                    d.document_id,
                    serde_json::to_string(&d)?,
                    d.published_timestamp.nanos()?,
                    d.last_updated.nanos()?
                ],
            )?;
        }
        tx.execute(
            "INSERT INTO settings VALUES(?,?) ON CONFLICT(key) DO NOTHING",
            params![APPLICATION_KEY, target],
        )?;
        tx.execute(
            "INSERT INTO settings VALUES('migration-v1',?)",
            [serde_json::to_string(&result)?],
        )?;
        tx.commit()?;
        Ok(result)
    }
}
fn known_keys(value: &Value, allowed: &[&str]) -> Result<()> {
    let obj = value
        .as_object()
        .ok_or_else(|| anyhow::anyhow!("expected object"))?;
    for key in obj.keys() {
        ensure!(
            allowed.contains(&key.as_str()),
            "unknown migration field {key}"
        );
    }
    Ok(())
}
fn valid_document_id(v: &str) -> bool {
    !v.is_empty()
        && v.len() <= 256
        && !v
            .chars()
            .any(|c| c.is_whitespace() || c.is_control() || c == '/' || c == '\\')
}
fn valid_web_url(v: &str, required: bool) -> bool {
    if v.is_empty() {
        return !required;
    }
    url::Url::parse(v).is_ok_and(|u| {
        matches!(u.scheme(), "http" | "https")
            && u.host_str().is_some()
            && u.username().is_empty()
            && u.password().is_none()
    })
}
fn valid_timestamp(t: &Timestamp, required: bool) -> Result<()> {
    if t.is_zero() {
        ensure!(!required, "required timestamp is zero");
        return Ok(());
    }
    ensure!(
        t.parse()?.timestamp_nanos_opt().is_some(),
        "timestamp outside supported range"
    );
    Ok(())
}
