use crate::{
    models::{DealInfo, ThreadContext},
    time::Timestamp,
};
use anyhow::{Result, ensure};
use scraper::{ElementRef, Html, Selector};
use serde_json::Value;
use std::collections::BTreeMap;

pub struct Selectors {
    values: BTreeMap<String, Selector>,
}
impl Selectors {
    pub fn load() -> Result<Self> {
        let path = std::env::var("SELECTORS_CONFIG_PATH").unwrap_or_default();
        if path.trim().is_empty() {
            Self::from_bytes(include_bytes!("selectors.json"))
        } else {
            Self::from_bytes(&std::fs::read(path.trim())?)
        }
    }
    pub fn defaults() -> Self {
        Self::from_bytes(include_bytes!("selectors.json")).expect("embedded selectors")
    }
    pub fn from_bytes(raw: &[u8]) -> Result<Self> {
        let json: Value = serde_json::from_slice(raw)?;
        let mut values = BTreeMap::new();
        for (section, keys) in [
            ("container", &["item", "ignore_modifier"][..]),
            (
                "elements",
                &[
                    "title_link",
                    "title_text",
                    "retailer",
                    "posted_time",
                    "thread_image",
                    "like_count",
                    "comment_count",
                    "comment_count_fallback",
                    "view_count",
                ][..],
            ),
        ] {
            for key in keys {
                if let Some(value) = json["hot_deals_list"][section][key]
                    .as_str()
                    .filter(|v| !v.is_empty())
                {
                    values.insert(
                        (*key).into(),
                        Selector::parse(value)
                            .map_err(|e| anyhow::anyhow!("invalid selector {key}: {e:?}"))?,
                    );
                }
            }
        }
        for key in ["primary_link", "fallback_link", "category"] {
            if let Some(value) = json["deal_details"][key].as_str().filter(|v| !v.is_empty()) {
                values.insert(
                    key.into(),
                    Selector::parse(value)
                        .map_err(|e| anyhow::anyhow!("invalid selector {key}: {e:?}"))?,
                );
            }
        }
        for key in ["item", "title_link", "posted_time"] {
            ensure!(values.contains_key(key), "missing required selector {key}");
        }
        Ok(Self { values })
    }
    fn first<'a>(&self, e: ElementRef<'a>, key: &str) -> Option<ElementRef<'a>> {
        self.values.get(key).and_then(|s| e.select(s).next())
    }
    fn text(&self, e: ElementRef<'_>, key: &str) -> String {
        self.first(e, key).map(text).unwrap_or_default()
    }
}
fn text(e: ElementRef<'_>) -> String {
    e.text().collect::<String>().trim().into()
}
static CSS: std::sync::LazyLock<BTreeMap<&'static str, Selector>> =
    std::sync::LazyLock::new(|| {
        [
            "#rfd_topic_summary",
            ".retailer_badge",
            ".savings",
            "[data-dealer-name]",
            "a",
            "dt",
            "script[type='application/ld+json']",
            "span",
            "time",
        ]
        .into_iter()
        .map(|s| (s, Selector::parse(s).expect("static CSS")))
        .collect()
    });
fn select(s: &str) -> &'static Selector {
    &CSS[s]
}
fn find_text(e: ElementRef<'_>, s: &str) -> String {
    e.select(select(s)).next().map(text).unwrap_or_default()
}
fn clean(raw: &str) -> String {
    raw.split_whitespace().collect::<Vec<_>>().join(" ")
}
pub fn retailer(raw: &str) -> String {
    let mut v = clean(raw);
    while v.to_lowercase().starts_with("at ") {
        v = v[3..].trim().into();
    }
    if v.len().is_multiple_of(2) {
        let mid = v.len() / 2;
        if v.is_char_boundary(mid) && v[..mid].eq_ignore_ascii_case(&v[mid..]) {
            return v[..mid].trim().into();
        }
    }
    let parts: Vec<_> = v.split_whitespace().collect();
    if parts.len() % 2 == 0 {
        let mid = parts.len() / 2;
        let left = parts[..mid].join(" ");
        if left.eq_ignore_ascii_case(&parts[mid..].join(" ")) {
            return left;
        }
    }
    v
}
pub fn list(html: &str, base: &str, s: &Selectors) -> Result<Vec<DealInfo>> {
    let doc = Html::parse_document(html);
    let items: Vec<_> = doc.select(&s.values["item"]).collect();
    ensure!(
        !items.is_empty(),
        "no RFD list elements found; possible block or page structure change"
    );
    let mut out = Vec::new();
    for card in items {
        if s.values
            .get("ignore_modifier")
            .is_some_and(|v| v.matches(&card))
        {
            continue;
        }
        if s.values.contains_key("title_text") && s.first(card, "title_text").is_none() {
            continue;
        }
        let mut d = DealInfo::default();
        if let Some(mut time) = s.first(card, "posted_time") {
            if time.value().name() != "time"
                && let Some(child) = time.select(select("time")).next()
            {
                time = child;
            }
            if let Some(raw) = time.attr("datetime")
                && let Ok(t) = chrono::DateTime::parse_from_rfc3339(raw)
            {
                d.published_timestamp = Timestamp::from_fixed(t);
            }
        }
        if let Some(mut link) = s.first(card, "title_link") {
            if link.value().name() != "a"
                && let Some(child) = link.select(select("a")).next()
            {
                link = child;
            }
            if link.value().name() == "a" {
                d.title = text(link);
                if let Some(href) = link.attr("href") {
                    d.post_url = if href.starts_with('/') {
                        format!("{}{href}", base.trim_end_matches('/'))
                    } else {
                        href.into()
                    };
                }
            }
        }
        if s.values.contains_key("title_text") {
            d.title = s.text(card, "title_text");
        }
        if !d.title.is_empty() {
            d.post_url = normalize_post_url(&d.post_url);
        } else {
            d.post_url.clear();
        }
        d.retailer = retailer(&s.text(card, "retailer"));
        if d.retailer.is_empty() {
            d.retailer = retailer(
                card.attr("data-dealer-name")
                    .or_else(|| {
                        card.select(select("[data-dealer-name]"))
                            .next()
                            .and_then(|e| e.attr("data-dealer-name"))
                    })
                    .unwrap_or(""),
            );
        }
        if let Some(src) = s.first(card, "thread_image").and_then(|e| e.attr("src"))
            && (src.starts_with("https://") || src.starts_with("http://"))
        {
            d.thread_image_url = src.into();
        }
        let likes = s.text(card, "like_count");
        static SIGNED: std::sync::LazyLock<regex::Regex> =
            std::sync::LazyLock::new(|| regex::Regex::new(r"-?\d+").unwrap());
        let re = &*SIGNED;
        let like_count = re
            .find(&likes)
            .and_then(|v| v.as_str().parse().ok())
            .unwrap_or(0);
        let comments = if let Some(e) = s.first(card, "comment_count") {
            text(e)
        } else {
            s.text(card, "comment_count_fallback")
        };
        let comment_count = digits(&comments);
        let view = s.first(card, "view_count");
        let (view_count, view_count_available) =
            view.map(|e| (digits(&text(e)), true)).unwrap_or_default();
        let mut thread = ThreadContext {
            post_url: d.post_url.clone(),
            like_count,
            comment_count,
            view_count,
            view_count_available,
            ..Default::default()
        };
        if let Some(price) = card.select(select(".savings")).next() {
            d.price = price
                .children()
                .filter_map(|n| n.value().as_text().map(|t| t.text.to_string()))
                .collect::<String>()
                .trim()
                .into();
            d.original_price = price
                .select(select("span"))
                .map(text)
                .collect::<String>()
                .trim()
                .into();
        }
        thread.document_id = d.document_id.clone();
        d.threads.push(thread);
        out.push(d);
    }
    Ok(out)
}
fn digits(raw: &str) -> i64 {
    raw.chars()
        .filter(char::is_ascii_digit)
        .collect::<String>()
        .parse()
        .unwrap_or(0)
}
fn normalize_post_url(raw: &str) -> String {
    let Ok(mut u) = url::Url::parse(raw) else {
        return raw.into();
    };
    let host = u.host_str().unwrap_or("");
    if !matches!(
        host,
        "redflagdeals.com" | "forums.redflagdeals.com" | "www.redflagdeals.com"
    ) {
        return raw.into();
    }
    let host = host.trim_start_matches("www.");
    let host = if host == "redflagdeals.com" {
        "forums.redflagdeals.com"
    } else {
        host
    }
    .to_string();
    let _ = u.set_scheme("https");
    let _ = u.set_host(Some(&host));
    if u.path().len() > 1 && u.path().ends_with('/') {
        let path = u.path().trim_end_matches('/').to_string();
        u.set_path(&path);
    }
    let mut q: Vec<(String, String)> = u
        .query_pairs()
        .filter(|(k, _)| {
            !matches!(
                k.as_ref(),
                "utm_source"
                    | "utm_medium"
                    | "utm_campaign"
                    | "utm_term"
                    | "utm_content"
                    | "rfd_sk"
                    | "sd"
                    | "sk"
            )
        })
        .map(|(a, b)| (a.into_owned(), b.into_owned()))
        .collect();
    q.sort_by(|a, b| a.0.cmp(&b.0));
    u.set_query(None);
    if !q.is_empty() {
        u.query_pairs_mut().extend_pairs(q);
    }
    u.to_string()
}
fn web_url(raw: &str) -> bool {
    url::Url::parse(raw).is_ok_and(|u| {
        matches!(u.scheme(), "http" | "https")
            && u.host_str().is_some()
            && u.username().is_empty()
            && u.password().is_none()
    })
}
pub fn validate(d: &DealInfo) -> Result<()> {
    ensure!(
        !d.title.trim().is_empty() && !d.published_timestamp.is_zero(),
        "deal requires title and publication time"
    );
    d.published_timestamp.parse()?;
    ensure!(web_url(&d.post_url), "invalid post URL");
    for u in [&d.actual_deal_url, &d.thread_image_url] {
        ensure!(u.is_empty() || web_url(u), "invalid product or image URL");
    }
    Ok(())
}
pub fn assign_id(d: &mut DealInfo) -> Result<()> {
    use sha2::{Digest, Sha256};
    validate(d)?;
    d.document_id = hex::encode(Sha256::digest(d.published_timestamp.0.as_bytes()));
    if let Some(t) = d.threads.first_mut() {
        t.document_id = d.document_id.clone();
    }
    Ok(())
}
#[derive(Debug, Default, Clone, serde::Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct Detail {
    pub deal_link: String,
    pub description: String,
    pub comments: String,
    pub summary: String,
    pub price: String,
    pub original_price: String,
    pub savings: String,
    pub retailer: String,
    pub category: String,
}
pub fn detail(html: &str, s: &Selectors) -> Result<Detail> {
    let doc = Html::parse_document(html);
    let root = doc.root_element();
    let mut out = Detail::default();
    for key in ["primary_link", "fallback_link"] {
        if let Some(sel) = s.values.get(key) {
            for link in root.select(sel) {
                let href = link.attr("href").unwrap_or("").trim();
                if let Ok(u) = url::Url::parse(href) {
                    let host = u.host_str().unwrap_or("").to_lowercase();
                    if matches!(u.scheme(), "http" | "https")
                        && !host.is_empty()
                        && host != "redflagdeals.com"
                        && !host.ends_with(".redflagdeals.com")
                    {
                        out.deal_link = href.into();
                        break;
                    }
                }
            }
            if !out.deal_link.is_empty() {
                break;
            }
        }
    }
    let (mut ld_price, mut ld_retailer) = (String::new(), String::new());
    for script in root.select(select("script[type='application/ld+json']")) {
        let Ok(json) = serde_json::from_str::<Value>(&text(script)) else {
            continue;
        };
        let postings = if let Some(items) = json.as_array() {
            items.clone()
        } else {
            vec![json]
        };
        for p in postings {
            if p["@type"] != "DiscussionForumPosting" {
                continue;
            }
            out.description = html_text(p["text"].as_str().unwrap_or(""));
            let mut comments = Vec::new();
            if let Some(items) = p["comment"].as_array() {
                for c in items {
                    comments.push(format!("- {}", html_text(c["text"].as_str().unwrap_or(""))));
                }
            }
            out.comments = comments.join("\n");
            if out.comments.len() > 2000 {
                let mut end = 2000;
                while !out.comments.is_char_boundary(end) {
                    end -= 1;
                }
                out.comments.truncate(end);
                out.comments.push_str("...(truncated)");
            }
            ld_price = json_scalar(&p["about"]["offers"]["price"]);
            if !ld_price.is_empty() && p["about"]["offers"]["priceCurrency"] == "CAD" {
                ld_price = format!("${ld_price}");
            }
            ld_retailer = json_scalar(&p["about"]["brand"]["name"]);
            break;
        }
    }
    out.summary = find_text(root, "#rfd_topic_summary");
    for dt in root.select(select("dt")) {
        let value = next_text(dt);
        match text(dt).as_str() {
            "Price:" => out.price = value,
            "Original Price:" => out.original_price = value,
            "Savings:" => out.savings = value,
            _ => {}
        }
    }
    if out.price.is_empty() {
        out.price = ld_price;
    }
    out.retailer = retailer(&find_text(root, ".retailer_badge"));
    if out.retailer.is_empty() {
        for dt in root.select(select("dt")) {
            if text(dt) == "Retailer:" {
                out.retailer = retailer(&next_text(dt));
            }
        }
    }
    if out.retailer.is_empty() {
        out.retailer = retailer(&ld_retailer);
    }
    if let Some(sel) = s.values.get("category") {
        out.category = root
            .select(sel)
            .map(|element| crate::category::normalize(&text(element)))
            .find(|value| !value.is_empty())
            .unwrap_or_default();
    }
    if out.category.is_empty() {
        for dt in root.select(select("dt")) {
            if text(dt) == "Category:" {
                out.category = crate::category::normalize(&next_text(dt));
            }
        }
    }
    Ok(out)
}
fn json_scalar(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        Value::Number(n) => n.to_string(),
        _ => String::new(),
    }
}
fn next_text(e: ElementRef<'_>) -> String {
    let mut n = e.next_sibling();
    while let Some(node) = n {
        if let Some(e) = ElementRef::wrap(node) {
            return text(e);
        }
        n = node.next_sibling();
    }
    String::new()
}
fn html_text(raw: &str) -> String {
    Html::parse_document(raw)
        .root_element()
        .text()
        .collect::<String>()
        .trim()
        .into()
}
