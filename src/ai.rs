//! Optional title cleanup; provider failures preserve ordinary deal titles.
use crate::{
    control::Control,
    db::Db,
    http,
    models::{DealInfo, GeminiQuotaStatus},
    time::Timestamp,
};
use anyhow::{Context, Result, ensure};
use serde::Deserialize;
use serde_json::{Value, json};
use std::{collections::BTreeMap, time::Duration};
use tokio::sync::Mutex;
#[derive(Default)]
struct State {
    quota: GeminiQuotaStatus,
    key: usize,
    model: usize,
    rate_limits: usize,
    timeouts: usize,
    requests: usize,
    input: usize,
    output: usize,
    retries: usize,
    parse_failures: usize,
}
pub struct Cleaner {
    db: Db,
    http: reqwest::Client,
    base: String,
    keys: Vec<String>,
    models: Vec<String>,
    state: Mutex<State>,
    free_only: bool,
}
#[derive(Deserialize)]
#[serde(default)]
#[derive(Default)]
pub struct Title {
    pub index: usize,
    pub clean_title: String,
}
#[derive(Clone)]
pub struct Request {
    pub index: usize,
    pub title: String,
    pub retailer: String,
    pub price: String,
}
fn pacific_day() -> String {
    chrono::Utc::now()
        .with_timezone(&chrono_tz::America::Los_Angeles)
        .format("%Y-%m-%d")
        .to_string()
}
pub fn free_ready(quota: &GeminiQuotaStatus) -> bool {
    if quota.current_day != pacific_day() {
        return true;
    }
    quota.daily_requests < 20
        && (!quota.all_exhausted
            || quota.exhausted_at.parse().is_ok_and(|t| {
                chrono::Utc::now().signed_duration_since(t) >= chrono::Duration::minutes(30)
            }))
        && (quota.last_request_at.is_zero()
            || quota.last_request_at.parse().is_ok_and(|t| {
                chrono::Utc::now().signed_duration_since(t) >= chrono::Duration::seconds(60)
            }))
}
impl Cleaner {
    pub async fn new(db: Db, keys: Vec<String>, models: Vec<String>) -> Result<Option<Self>> {
        if keys.is_empty() {
            return Ok(None);
        }
        Ok(Some(
            Self::with_base(
                db,
                keys,
                models,
                "https://generativelanguage.googleapis.com/v1beta/models".into(),
            )
            .await?,
        ))
    }
    pub async fn with_base(
        db: Db,
        keys: Vec<String>,
        models: Vec<String>,
        base: String,
    ) -> Result<Self> {
        Self::build(db, keys, models, base, false).await
    }
    pub async fn free_tier(db: Db, key: String, model: String) -> Result<Self> {
        Self::with_free_base(
            db,
            key,
            model,
            "https://generativelanguage.googleapis.com/v1beta/models".into(),
        )
        .await
    }
    pub async fn with_free_base(db: Db, key: String, model: String, base: String) -> Result<Self> {
        Self::build(db, vec![key], vec![model], base, true).await
    }
    async fn build(
        db: Db,
        keys: Vec<String>,
        models: Vec<String>,
        base: String,
        free_only: bool,
    ) -> Result<Self> {
        ensure!(
            !keys.is_empty() && !models.is_empty(),
            "Gemini requires API keys and model IDs"
        );
        ensure!(
            models.iter().all(|m| !m.is_empty()
                && m.bytes()
                    .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'.' | b'_'))),
            "invalid Gemini model ID"
        );
        let quota = match db.call(|s| s.quota()).await {
            Ok(q) => q.unwrap_or_default(),
            Err(_) if free_only => {
                anyhow::bail!("could not restore free Gemini request budget");
            }
            Err(_) => {
                tracing::warn!("could not restore Gemini quota state");
                GeminiQuotaStatus::default()
            }
        };
        let key = quota
            .current_location
            .strip_prefix("key")
            .and_then(|s| s.parse::<usize>().ok())
            .filter(|i| *i < keys.len())
            .unwrap_or(0);
        let model = models
            .iter()
            .position(|m| m == &quota.current_model)
            .unwrap_or(0);
        let c = Self {
            db,
            http: http::client(Duration::from_secs(30), false, Some(Vec::new()))?,
            base,
            keys,
            models,
            free_only,
            state: Mutex::new(State {
                quota,
                key,
                model,
                ..Default::default()
            }),
        };
        let mut s = c.state.lock().await;
        c.reset_if_expired(&mut s);
        ensure!(
            c.save(&mut s).await || !free_only,
            "could not persist free Gemini budget"
        );
        drop(s);
        Ok(c)
    }
    fn reset_if_expired(&self, s: &mut State) {
        let q = &s.quota;
        let expired = q.all_exhausted
            && !q.exhausted_at.is_zero()
            && q.exhausted_at.parse().is_ok_and(|t| {
                chrono::Utc::now().signed_duration_since(t) >= chrono::Duration::minutes(30)
            });
        if q.current_day != pacific_day() {
            s.quota = GeminiQuotaStatus {
                current_day: pacific_day(),
                ..Default::default()
            };
        } else if expired {
            // Cooldown expiry never resets the persistent daily request cap.
            s.quota.all_exhausted = false;
            s.quota.exhausted_at = Timestamp::default();
        } else {
            return;
        }
        s.key = 0;
        s.model = 0;
        s.rate_limits = 0;
        s.timeouts = 0;
    }
    async fn save(&self, s: &mut State) -> bool {
        s.quota.current_model = self.models[s.model].clone();
        s.quota.current_location = format!("key{}", s.key);
        s.quota.last_updated = Timestamp::now();
        let q = s.quota.clone();
        if self
            .db
            .call_until(Duration::from_secs(5), move |s| s.save_quota(&q))
            .await
            .is_err()
        {
            tracing::warn!("could not persist Gemini quota state");
            return false;
        }
        true
    }
    async fn next_model(&self, s: &mut State) -> bool {
        if s.model + 1 < self.models.len() {
            s.model += 1;
        } else if s.key + 1 < self.keys.len() {
            s.key += 1;
            s.model = 0;
            s.rate_limits = 0;
            s.timeouts = 0;
        } else {
            s.quota.all_exhausted = true;
            s.quota.exhausted_at = Timestamp::now();
            self.save(s).await;
            return false;
        }
        self.save(s).await;
        true
    }
    async fn generate(&self, prompt: &str, control: &Control, s: &mut State) -> Result<Vec<Title>> {
        let mut last = anyhow::anyhow!("Gemini unavailable");
        for attempt in 0..4 {
            control.check()?;
            self.reset_if_expired(s);
            ensure!(!s.quota.all_exhausted, "Gemini quota cooldown active");
            if self.free_only {
                ensure!(
                    s.quota.daily_requests < 20,
                    "free Gemini daily request cap reached"
                );
                ensure!(
                    prompt.len() <= 16 * 1024,
                    "free Gemini prompt exceeds size cap"
                );
                ensure!(
                    s.quota.last_request_at.is_zero()
                        || s.quota
                            .last_request_at
                            .parse()
                            .is_ok_and(|t| chrono::Utc::now().signed_duration_since(t)
                                >= chrono::Duration::seconds(60)),
                    "free Gemini request interval active"
                );
                s.quota.daily_requests += 1;
                s.quota.last_request_at = Timestamp::now();
                // Reserve durably before contacting the provider. Failed calls count.
                ensure!(self.save(s).await, "could not reserve free Gemini request");
            }
            s.requests += 1;
            if attempt > 0 {
                s.retries += 1;
            }
            let url = format!(
                "{}/{}:generateContent",
                self.base.trim_end_matches('/'),
                self.models[s.model]
            );
            let mut body = json!({"contents":[{"role":"user","parts":[{"text":prompt}]}],"generationConfig":{"temperature":0.1,"responseMimeType":"application/json"}});
            if self.free_only {
                body["generationConfig"]["maxOutputTokens"] = json!(2048);
            }
            let call = control.with_budget(Duration::from_secs(30));
            let response = call
                .run(async {
                    let response = self
                        .http
                        .post(url)
                        .header("x-goog-api-key", &self.keys[s.key])
                        .json(&body)
                        .send()
                        .await
                        .map_err(|e| {
                            anyhow::anyhow!(if e.is_timeout() {
                                "Gemini request timeout"
                            } else {
                                "Gemini transport failed"
                            })
                        })?;
                    let status = response.status().as_u16();
                    let raw = http::body(response, 1024 * 1024)
                        .await
                        .map_err(|_| anyhow::anyhow!("Gemini response too large or unreadable"))?;
                    let value: Value = serde_json::from_slice(&raw)
                        .map_err(|_| anyhow::anyhow!("invalid Gemini response JSON"))?;
                    Ok::<_, anyhow::Error>((status, value))
                })
                .await;
            let (status, value) = match response {
                Ok(Ok(v)) => v,
                Ok(Err(e)) | Err(e) => {
                    last = e;
                    if last.to_string().contains("timeout") || last.to_string().contains("deadline")
                    {
                        s.timeouts += 1;
                    } else if !last.to_string().contains("transport") {
                        return Err(last);
                    }
                    if s.timeouts >= 5 {
                        s.timeouts = 0;
                        if s.key + 1 < self.keys.len() {
                            s.key += 1;
                            s.model = 0;
                            s.rate_limits = 0;
                            self.save(s).await;
                        }
                    }
                    if attempt < 3 {
                        control.run(tokio::time::sleep(backoff(attempt))).await?;
                        continue;
                    }
                    break;
                }
            };
            let mut extra = Duration::ZERO;
            if (200..300).contains(&status) {
                s.rate_limits = 0;
                s.timeouts = 0;
                s.input += value["usageMetadata"]["promptTokenCount"]
                    .as_u64()
                    .unwrap_or(0) as usize;
                s.output += value["usageMetadata"]["candidatesTokenCount"]
                    .as_u64()
                    .unwrap_or(0) as usize;
                match parse_titles(&value) {
                    Ok(titles) => return Ok(titles),
                    Err(e) => {
                        last = e;
                        s.parse_failures += 1;
                    }
                }
            } else {
                last = anyhow::anyhow!("Gemini HTTP status {status}");
                if self.free_only && matches!(status, 400 | 401 | 402 | 403 | 404 | 429) {
                    s.quota.all_exhausted = true;
                    s.quota.exhausted_at = Timestamp::now();
                    self.save(s).await;
                    return Err(last);
                }
                match status {
                    429 | 404 => {
                        s.rate_limits += 1;
                        if s.rate_limits < 3 {
                            extra = Duration::from_secs(5);
                        } else {
                            s.rate_limits = 0;
                            ensure!(self.next_model(s).await, "Gemini quota cooldown active");
                        }
                    }
                    400 => {
                        let message = value["error"]["message"].as_str().unwrap_or("");
                        if message.contains("is not supported") || message.contains("not available")
                        {
                            ensure!(self.next_model(s).await, "Gemini quota cooldown active");
                        } else {
                            return Err(last);
                        }
                    }
                    504 => {
                        s.timeouts += 1;
                        if s.timeouts >= 5 {
                            s.timeouts = 0;
                            if s.key + 1 < self.keys.len() {
                                s.key += 1;
                                s.model = 0;
                                s.rate_limits = 0;
                                self.save(s).await;
                            }
                        }
                    }
                    500..=599 => {}
                    _ => return Err(last),
                }
            }
            if attempt < 3 {
                control
                    .run(tokio::time::sleep(extra + backoff(attempt)))
                    .await?;
            }
        }
        Err(last)
    }
    async fn titles(
        &self,
        requests: &[Request],
        control: &Control,
        s: &mut State,
    ) -> BTreeMap<usize, String> {
        let mut pending = requests.to_vec();
        let mut completed = BTreeMap::new();
        for pass in 0..2 {
            if pending.is_empty() {
                break;
            }
            let extracted = match self.generate(&prompt(&pending, pass > 0), control, s).await {
                Ok(v) => v,
                Err(_) => {
                    tracing::warn!(
                        completed = completed.len(),
                        "optional title cleanup failed; preserving completed titles"
                    );
                    break;
                }
            };
            for title in extracted {
                if pending.iter().any(|p| p.index == title.index)
                    && !title.clean_title.trim().is_empty()
                {
                    completed.insert(title.index, title.clean_title.trim().to_owned());
                }
            }
            pending.retain(|p| !completed.contains_key(&p.index));
        }
        completed
    }
    pub async fn clean(&self, deals: &mut [DealInfo], control: &Control) {
        let control = control.with_budget(Duration::from_secs(30));
        let pending: Vec<_> = deals
            .iter()
            .enumerate()
            .filter(|(_, d)| !d.ai_processed || d.clean_title.is_empty())
            .map(|(i, _)| i)
            .collect();
        let mut s = self.state.lock().await;
        for batch in pending.chunks(10) {
            if control.check().is_err() {
                break;
            }
            let requests = batch
                .iter()
                .enumerate()
                .map(|(index, &i)| Request {
                    index,
                    title: deals[i].title.clone(),
                    retailer: deals[i].retailer.clone(),
                    price: deals[i].price.clone(),
                })
                .collect::<Vec<_>>();
            let results = self.titles(&requests, &control, &mut s).await;
            if self.free_only && results.is_empty() {
                break;
            }
            for (index, &i) in batch.iter().enumerate() {
                if let Some(title) = results.get(&index) {
                    deals[i].clean_title = title.clone();
                    deals[i].ai_processed = true;
                }
            }
        }
        tracing::info!(
            requests = s.requests,
            input_tokens = s.input,
            output_tokens = s.output,
            retries = s.retries,
            parse_failures = s.parse_failures,
            "optional title cleanup usage"
        );
        s.requests = 0;
        s.input = 0;
        s.output = 0;
        s.retries = 0;
        s.parse_failures = 0;
    }
}
fn backoff(attempt: u32) -> Duration {
    let base = 1u64 << attempt;
    Duration::from_millis(base * 1000 + rand::random_range(0..base * 500))
}
pub fn parse_titles(response: &Value) -> Result<Vec<Title>> {
    let parts = response["candidates"][0]["content"]["parts"]
        .as_array()
        .context("Gemini returned no title content")?;
    let mut combined = String::new();
    for part in parts {
        if part["thought"] == true {
            continue;
        }
        let Some(text) = part["text"].as_str().filter(|t| !t.is_empty()) else {
            continue;
        };
        if let Ok(titles) = serde_json::from_str(strip(text)) {
            return Ok(titles);
        }
        combined.push_str(text);
    }
    serde_json::from_str(strip(&combined))
        .map_err(|_| anyhow::anyhow!("could not parse Gemini title response"))
}
fn strip(text: &str) -> &str {
    let text = text.trim();
    let text = text
        .strip_prefix("```json")
        .or_else(|| text.strip_prefix("```"))
        .unwrap_or(text)
        .trim();
    let text = text.strip_suffix("```").unwrap_or(text).trim();
    let Some(start) = text.find(['[', '{']) else {
        return text;
    };
    let mut depth = 0;
    let mut quoted = false;
    let mut escaped = false;
    for (index, c) in text[start..].char_indices() {
        if quoted {
            if escaped {
                escaped = false;
            } else if c == '\\' {
                escaped = true;
            } else if c == '"' {
                quoted = false;
            }
            continue;
        }
        match c {
            '"' => quoted = true,
            '[' | '{' => depth += 1,
            ']' | '}' => {
                depth -= 1;
                if depth == 0 {
                    return &text[start..start + index + c.len_utf8()];
                }
            }
            _ => {}
        }
    }
    text
}
pub fn prompt(requests: &[Request], repair: bool) -> String {
    let mut out: String = if repair {
        "The previous response omitted some indexes. Clean ONLY these missing deal titles. ".into()
    } else {
        "Clean these deal titles. ".into()
    };
    out += "For each input, create a concise title (5-15 words). Remove fluff (\"Lava Hot\", \"Price Error\", \"YMMV\", emojis), store names if redundant, and focus on the product and price/discount.\n\n";
    for r in requests {
        out += &format!("{}. Title: \"{}\"", r.index, r.title);
        if !r.retailer.is_empty() {
            out += &format!(" | Retailer: \"{}\"", r.retailer);
        }
        if !r.price.is_empty() {
            out += &format!(" | Price: \"{}\"", r.price);
        }
        out.push('\n');
    }
    out += "\nReturn JSON only. Return exactly one object for every input index above, no omissions and no extra indexes. Use the original input index values exactly: [{\"index\": 0, \"clean_title\": \"...\"}, ...]";
    out
}
