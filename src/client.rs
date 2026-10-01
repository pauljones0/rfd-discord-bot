use crate::{
    http,
    models::DealInfo,
    parse::{self, Selectors},
    reconcile::primary_url,
    urls,
};
use anyhow::{Context, Result, ensure};
use futures_util::{StreamExt, stream};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeMap,
    sync::{Arc, LazyLock},
    time::Duration,
};

pub struct Client {
    http: reqwest::Client,
    jar: Arc<reqwest::cookie::Jar>,
    hosts: Vec<String>,
    list_url: url::Url,
    selectors: Selectors,
    amazon: String,
    bestbuy: String,
}
#[derive(Debug)]
struct Status(u16);
impl std::fmt::Display for Status {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "RFD HTTP status {}", self.0)
    }
}
impl std::error::Error for Status {}
#[derive(Default, Debug)]
pub struct DetailStats {
    pub attempted: usize,
    pub succeeded: usize,
    pub failed: usize,
    pub not_found: usize,
}
impl Client {
    pub fn new(selectors: Selectors, amazon: String, bestbuy: String) -> Result<Self> {
        Self::with_source(
            selectors,
            amazon,
            bestbuy,
            "https://forums.redflagdeals.com/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d".into(),
            vec![
                "forums.redflagdeals.com".into(),
                "redflagdeals.com".into(),
                "www.redflagdeals.com".into(),
            ],
        )
    }
    /// Injected source for offline integration tests; never an environment override.
    pub fn with_source(
        selectors: Selectors,
        amazon: String,
        bestbuy: String,
        list: String,
        hosts: Vec<String>,
    ) -> Result<Self> {
        let jar = Arc::new(reqwest::cookie::Jar::default());
        Ok(Self {
            http: http::client_with_jar(
                Duration::from_secs(30),
                true,
                Some(hosts.clone()),
                Some(jar.clone()),
            )?,
            jar,
            hosts,
            list_url: url::Url::parse(&list)?,
            selectors,
            amazon,
            bestbuy,
        })
    }
    fn validate(&self, raw: &str) -> Result<url::Url> {
        let u = url::Url::parse(raw)?;
        ensure!(
            matches!(u.scheme(), "http" | "https")
                && u.username().is_empty()
                && u.password().is_none()
                && self.hosts.iter().any(|h| Some(h.as_str()) == u.host_str()),
            "RFD fetch URL is outside the allowed source hosts"
        );
        Ok(u)
    }
    fn request(&self, u: &url::Url) -> reqwest::RequestBuilder {
        // One internally consistent browser profile per persistent session.
        self.http.get(u.clone())
            .header("User-Agent","Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36")
            .header("Sec-Ch-Ua","\"Chromium\";v=\"134\", \"Google Chrome\";v=\"134\", \"Not:A-Brand\";v=\"24\"")
            .header("Sec-Ch-Ua-Mobile","?0").header("Sec-Ch-Ua-Platform","\"Linux\"")
            .header("Accept","text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
            .header("Accept-Language","en-US,en;q=0.9").header("Cache-Control","max-age=0").header("Upgrade-Insecure-Requests","1")
            .header("Sec-Fetch-Dest","document").header("Sec-Fetch-Mode","navigate").header("Sec-Fetch-Site","none").header("Sec-Fetch-User","?1")
    }
    async fn html(&self, raw: &str) -> Result<String> {
        let u = self.validate(raw)?;
        let mut response = self.request(&u).send().await?;
        if response.status().as_u16() == 202 {
            let bytes = http::body(response, 128 * 1024).await?;
            let challenge = Challenge::parse(std::str::from_utf8(&bytes)?)?;
            let (cookie, attempts) = challenge.solve().await?;
            let secure = if u.scheme() == "https" {
                "; Secure"
            } else {
                ""
            };
            self.jar.add_cookie_str(
                &format!(
                    "pow_bypass={cookie}; Path=/; Max-Age={}; SameSite=Lax{secure}",
                    challenge.seconds
                ),
                &u,
            );
            tracing::info!(attempts, "completed RFD source proof of work");
            response = self.request(&u).send().await?;
        }
        if response.status().as_u16() != 200 {
            return Err(Status(response.status().as_u16()).into());
        }
        String::from_utf8(http::body(response, http::MAX_BODY).await?)
            .context("RFD source is not UTF-8")
    }
    pub async fn list(&self) -> Result<Vec<DealInfo>> {
        let mut last = anyhow::anyhow!("RFD list unavailable");
        for attempt in 0..7 {
            let result = async {
                let html = self.html(self.list_url.as_str()).await?;
                parse::list(&html, self.list_url.as_str(), &self.selectors)
            }
            .await;
            match result {
                Ok(deals) => return Ok(deals),
                Err(e) => last = e,
            }
            if attempt == 6 || (attempt >= 3 && !transient_dns(&last)) {
                break;
            }
            tracing::warn!(attempt = attempt + 1, "RFD list fetch failed; retrying");
            tokio::time::sleep(retry_delay(attempt)).await;
        }
        Err(last)
    }
    async fn detail(&self, raw: &str) -> Result<parse::Detail> {
        let mut last = anyhow::anyhow!("RFD detail unavailable");
        for attempt in 0..3 {
            let result = async {
                let html = self.html(raw).await?;
                parse::detail(&html, &self.selectors)
            }
            .await;
            match result {
                Ok(detail) => return Ok(detail),
                Err(e) => last = e,
            }
            if attempt == 2 || !retry_detail(&last) {
                break;
            }
            tokio::time::sleep(retry_delay(attempt)).await;
        }
        Err(last)
    }
    pub async fn details(&self, deals: Vec<DealInfo>) -> (Vec<DealInfo>, DetailStats) {
        let results: Vec<_> = stream::iter(deals.into_iter().enumerate())
            .map(|(index, mut deal)| async move {
                if primary_url(&deal).is_empty() {
                    return (index, deal, 0);
                }
                let result = self.detail(primary_url(&deal)).await;
                match result {
                    Ok(d) => {
                        deal.actual_deal_url = urls::referral(
                            &urls::clean_product(&d.deal_link),
                            &self.amazon,
                            &self.bestbuy,
                        );
                        deal.description = d.description;
                        deal.comments = d.comments;
                        deal.summary = d.summary;
                        for (target, value) in [
                            (&mut deal.price, d.price),
                            (&mut deal.original_price, d.original_price),
                            (&mut deal.savings, d.savings),
                            (&mut deal.retailer, d.retailer),
                            (&mut deal.category, d.category),
                        ] {
                            if !value.is_empty() {
                                *target = value;
                            }
                        }
                        (index, deal, 1)
                    }
                    Err(e) => {
                        if e.downcast_ref::<Status>().is_some_and(|s| s.0 == 404) {
                            if let Some(thread) = deal.threads.first_mut() {
                                thread.not_found = true;
                            }
                            (index, deal, 2)
                        } else {
                            tracing::warn!("RFD detail fetch failed");
                            (index, deal, 3)
                        }
                    }
                }
            })
            .buffer_unordered(2)
            .collect()
            .await;
        let mut results = results;
        results.sort_by_key(|r| r.0);
        let mut stats = DetailStats::default();
        let mut deals = Vec::with_capacity(results.len());
        for (_, deal, status) in results {
            if status != 0 {
                stats.attempted += 1;
            }
            match status {
                1 => stats.succeeded += 1,
                2 => stats.not_found += 1,
                3 => stats.failed += 1,
                _ => {}
            }
            deals.push(deal);
        }
        (deals, stats)
    }
}
fn retry_delay(attempt: u32) -> Duration {
    let base = 1u64 << attempt;
    Duration::from_millis(base * 1000 + rand::random_range(0..base * 500))
}
fn transient_dns(error: &anyhow::Error) -> bool {
    error.chain().any(|e| {
        let s = e.to_string().to_lowercase();
        (s.contains("dns") || s.contains("lookup") || s.contains("name resolution"))
            && (s.contains("temporary")
                || s.contains("server misbehaving")
                || s.contains("try again")
                || s.contains("timed out"))
    })
}
fn retry_detail(error: &anyhow::Error) -> bool {
    if let Some(s) = error.downcast_ref::<Status>() {
        return matches!(s.0, 408 | 425 | 429 | 500..=599);
    }
    error.chain().any(|e| {
        e.downcast_ref::<reqwest::Error>()
            .is_some_and(|e| e.is_connect() || e.is_timeout() || e.is_request() || e.is_body())
    })
}
static FIELDS: LazyLock<regex::Regex> = LazyLock::new(|| {
    regex::Regex::new(r#"(?m)(challenge_nonce|challenge_hmac|difficulty|difficulty_char|issued_at|cookie_duration)\s*:\s*['"]([^'"]+)['"]"#).unwrap()
});
#[derive(Debug)]
pub struct Challenge {
    nonce: String,
    hmac: String,
    issued: String,
    difficulty: usize,
    digit: u8,
    seconds: u32,
}
impl Challenge {
    pub fn parse(body: &str) -> Result<Self> {
        ensure!(
            body.len() <= 128 * 1024 && body.contains("POW_CHALLENGE_DATA"),
            "unrecognized source proof of work"
        );
        let fields: BTreeMap<_, _> = FIELDS
            .captures_iter(body)
            .map(|m| (m[1].to_owned(), m[2].to_owned()))
            .collect();
        let field = |key: &str| {
            fields
                .get(key)
                .filter(|s| !s.trim().is_empty())
                .cloned()
                .with_context(|| format!("proof of work missing {key}"))
        };
        let nonce = field("challenge_nonce")?;
        let hmac = field("challenge_hmac")?;
        let issued = field("issued_at")?;
        ensure!(
            [&nonce, &hmac, &issued]
                .iter()
                .all(|s| !s.contains(['|', ';', '\r', '\n'])),
            "invalid source proof of work fields"
        );
        let difficulty: usize = field("difficulty")?.parse()?;
        ensure!(
            (1..=6).contains(&difficulty),
            "unsupported source proof of work difficulty"
        );
        let digit = field("difficulty_char")?.to_ascii_lowercase();
        ensure!(
            digit.len() == 1 && digit.as_bytes()[0].is_ascii_hexdigit(),
            "invalid source proof of work character"
        );
        let seconds = fields
            .get("cookie_duration")
            .map(|s| s.parse::<u32>())
            .transpose()?
            .unwrap_or(3600);
        ensure!(
            (1..=86400).contains(&seconds),
            "invalid source proof of work expiry"
        );
        Ok(Self {
            nonce,
            hmac,
            issued,
            difficulty,
            digit: u8::from_str_radix(&digit, 16)?,
            seconds,
        })
    }
    pub async fn solve(&self) -> Result<(String, usize)> {
        let mut prefix = Sha256::new();
        prefix.update(self.nonce.as_bytes());
        prefix.update(self.issued.as_bytes());
        for attempt in 1..=10_000_000usize {
            if attempt % 1024 == 0 {
                tokio::task::yield_now().await;
            }
            let mut number = [0u8; 20];
            let mut n = attempt;
            let mut start = number.len();
            while n > 0 {
                start -= 1;
                number[start] = b'0' + (n % 10) as u8;
                n /= 10;
            }
            let mut hash = prefix.clone();
            hash.update(&number[start..]);
            let hash = hash.finalize();
            if (0..self.difficulty).all(|i|if i%2==0{hash[i/2]>>4}else{hash[i/2]&15}==self.digit){
                return Ok((format!("{}|{}|{attempt}|{}|{}",self.nonce,self.issued,hex::encode(hash),self.hmac),attempt));
            }
        }
        anyhow::bail!("source proof of work exceeded its bounded attempt budget")
    }
}
