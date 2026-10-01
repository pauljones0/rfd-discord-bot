use crate::env::*;
use anyhow::{Result, ensure};
use std::{path::PathBuf, time::Duration};
#[derive(Clone)]
pub struct Config {
    pub token: String,
    pub app_id: String,
    pub guild_id: String,
    pub database: PathBuf,
    pub listen: String,
    pub poll_interval: Duration,
    pub poll_timeout: Duration,
    pub update_interval: Duration,
    pub max_deals: usize,
    pub amazon_tag: String,
    pub bestbuy_prefix: String,
    pub gemini_keys: Vec<String>,
    pub gemini_models: Vec<String>,
}
impl Config {
    pub fn load() -> Result<Self> {
        let c = Self {
            token: value("DISCORD_BOT_TOKEN", ""),
            app_id: value("DISCORD_APP_ID", ""),
            guild_id: value("DISCORD_GUILD_ID", ""),
            database: value("SQLITE_PATH", "data/rfd.sqlite").into(),
            listen: value("LISTEN_ADDR", "127.0.0.1:8080"),
            poll_interval: duration("RFD_POLL_INTERVAL", "3m", false)?,
            poll_timeout: duration("RFD_POLL_TIMEOUT", "4m", false)?,
            update_interval: duration("DISCORD_UPDATE_INTERVAL", "10m", false)?,
            max_deals: positive("MAX_STORED_DEALS", "2000")?,
            amazon_tag: value("AMAZON_AFFILIATE_TAG", ""),
            bestbuy_prefix: value("BESTBUY_AFFILIATE_PREFIX", ""),
            gemini_keys: csv(&value("GEMINI_API_KEY", "")),
            gemini_models: csv(&value("GEMINI_MODELS", "")),
        };
        ensure!(
            !c.token.is_empty() && !c.app_id.is_empty(),
            "DISCORD_BOT_TOKEN and DISCORD_APP_ID are required"
        );
        ensure!(
            c.gemini_keys.is_empty() || !c.gemini_models.is_empty(),
            "set GEMINI_MODELS to supported model IDs when enabling optional Gemini title cleanup"
        );
        if !c.bestbuy_prefix.is_empty() {
            let u = url::Url::parse(&c.bestbuy_prefix)?;
            ensure!(
                u.scheme() == "https" && u.host_str().is_some(),
                "BESTBUY_AFFILIATE_PREFIX must be an HTTPS URL prefix"
            );
        }
        Ok(c)
    }
}
