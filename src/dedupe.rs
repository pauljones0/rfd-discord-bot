use crate::{models::DealInfo, urls};
use std::{
    collections::{BTreeMap, BTreeSet},
    sync::LazyLock,
};
static TOKEN: LazyLock<regex::Regex> =
    LazyLock::new(|| regex::Regex::new(r"[a-z0-9]+(?:\.[a-z0-9]+)*").unwrap());
static CAPACITY: LazyLock<regex::Regex> =
    LazyLock::new(|| regex::Regex::new(r"(?i)\b([0-9]+(?:\.[0-9]+)?)\s*(tb|gb|mb|mah)\b").unwrap());
static MODEL_NUMBER: LazyLock<regex::Regex> =
    LazyLock::new(|| regex::Regex::new(r"[0-9]+").unwrap());
const STOP: &[&str] = &[
    "the",
    "and",
    "for",
    "with",
    "sale",
    "plan",
    "month",
    "mo",
    "canada",
    "deal",
    "off",
    "discount",
    "new",
    "free",
    "best",
    "price",
    "buy",
    "get",
    "now",
    "hot",
    "limited",
    "time",
    "offer",
    "save",
    "online",
    "only",
    "shop",
    "available",
    "from",
    "down",
    "drop",
    "great",
];
const NOISE: &[&str] = &[
    "www", "com", "ca", "org", "net", "html", "htm", "php", "aspx", "en", "fr",
];
fn words(raw: &str) -> Vec<String> {
    TOKEN
        .find_iter(&raw.to_lowercase())
        .map(|m| m.as_str().into())
        .collect()
}
fn valuable(s: &str) -> bool {
    s.len() >= 2 && !STOP.contains(&s) && (s.bytes().any(|b| b.is_ascii_digit()) || s.len() >= 3)
}
pub fn tokens(d: &DealInfo) -> Vec<String> {
    let title = if d.clean_title.is_empty() {
        &d.title
    } else {
        &d.clean_title
    };
    let mut seen = BTreeSet::new();
    let mut out = Vec::new();
    for word in words(title) {
        if valuable(&word) && seen.insert(word.clone()) {
            out.push(word);
        }
    }
    let url = urls::canonical(&d.actual_deal_url);
    let url = url.replace('+', " ");
    let url = percent_encoding::percent_decode_str(&url).decode_utf8_lossy();
    let url = url
        .trim_start_matches("https://")
        .trim_start_matches("http://")
        .replace(['/', '?', '&', '=', '-', '_', '.'], " ");
    for word in words(&url) {
        if !NOISE.contains(&word.as_str()) && valuable(&word) && seen.insert(word.clone()) {
            out.push(word);
        }
    }
    out
}
pub fn prefer(candidate: &DealInfo, current: &DealInfo) -> bool {
    let a = !candidate.discord_message_ids.is_empty();
    let b = !current.discord_message_ids.is_empty();
    if a != b {
        return a;
    }
    let a = candidate
        .published_timestamp
        .parse()
        .expect("validated timestamp");
    let b = current
        .published_timestamp
        .parse()
        .expect("validated timestamp");
    if a != b {
        if candidate.published_timestamp.is_zero() {
            return false;
        }
        if current.published_timestamp.is_zero() {
            return true;
        }
        return a < b;
    }
    let a = candidate.threads.first();
    let b = current.threads.first();
    let al = a.map(|t| t.like_count).unwrap_or(0);
    let bl = b.map(|t| t.like_count).unwrap_or(0);
    if al != bl {
        return al > bl;
    }
    let ac = a.map(|t| t.comment_count).unwrap_or(0);
    let bc = b.map(|t| t.comment_count).unwrap_or(0);
    if ac != bc {
        return ac > bc;
    }
    candidate.document_id < current.document_id
}
// Index history without cloning its large payloads. Only a selected canonical
// record is cloned when ownership actually changes.
fn recent_by_url(recent: &[DealInfo]) -> BTreeMap<String, usize> {
    let mut out = BTreeMap::new();
    for (index, deal) in recent.iter().enumerate() {
        let key = urls::canonical(&deal.actual_deal_url);
        if key.is_empty() {
            continue;
        }
        match out.entry(key) {
            std::collections::btree_map::Entry::Vacant(entry) => {
                entry.insert(index);
            }
            std::collections::btree_map::Entry::Occupied(mut entry) => {
                if prefer(deal, &recent[*entry.get()]) {
                    entry.insert(index);
                }
            }
        }
    }
    out
}

fn variants(title: &str) -> BTreeMap<String, BTreeSet<String>> {
    let mut out: BTreeMap<String, BTreeSet<String>> = BTreeMap::new();
    for m in CAPACITY.captures_iter(title) {
        if let Ok(n) = m[1].parse::<f64>() {
            out.entry(format!("capacity:{}", m[2].to_lowercase()))
                .or_default()
                .insert(n.to_string());
        }
    }
    for token in words(title) {
        if token.as_bytes().first().is_some_and(u8::is_ascii_lowercase)
            && MODEL_NUMBER.is_match(&token)
        {
            out.entry(format!("model:{}", MODEL_NUMBER.replace_all(&token, "#")))
                .or_default()
                .insert(token);
        }
    }
    out
}
pub fn variant_conflict(left: &str, right: &str) -> bool {
    let left = variants(left);
    let right = variants(right);
    for (family, a) in left {
        if let Some(b) = right.get(&family)
            && a.difference(b).next().is_some()
            && b.difference(&a).next().is_some()
        {
            return true;
        }
    }
    false
}
pub fn fuzzy(left: &DealInfo, right: &DealInfo) -> bool {
    let a = urls::canonical(&left.actual_deal_url);
    let b = urls::canonical(&right.actual_deal_url);
    if !a.is_empty() && !b.is_empty() && a != b {
        return false;
    }
    let normalize = |s: &str| {
        s.split_whitespace()
            .collect::<Vec<_>>()
            .join(" ")
            .to_lowercase()
    };
    let retailer = normalize(&left.retailer);
    if retailer.is_empty()
        || retailer != normalize(&right.retailer)
        || variant_conflict(&left.title, &right.title)
    {
        return false;
    }
    let a = &left.search_tokens;
    let b = &right.search_tokens;
    if a.is_empty() || b.is_empty() {
        return false;
    }
    let overlap = a.iter().filter(|t| b.contains(t)).count();
    overlap >= 3 && overlap as f64 / a.len().min(b.len()) as f64 >= 0.75
}
pub fn deduplicate(
    mut scraped: Vec<DealInfo>,
    existing: &mut BTreeMap<String, DealInfo>,
    recent: &mut [DealInfo],
) -> Vec<DealInfo> {
    let by_url = recent_by_url(recent);
    let mut matched = BTreeSet::new();
    let mut out = Vec::new();
    for i in 0..scraped.len() {
        if matched.contains(&i) {
            continue;
        }
        let mut a = scraped[i].clone();
        if let Some(canonical) = by_url
            .get(&urls::canonical(&a.actual_deal_url))
            .map(|&index| &recent[index])
            .filter(|c| c.document_id != a.document_id)
        {
            a.document_id = canonical.document_id.clone();
            existing
                .entry(canonical.document_id.clone())
                .or_insert_with(|| canonical.clone());
            out.push(a);
            continue;
        }
        if existing.contains_key(&a.document_id) {
            out.push(a);
            continue;
        }
        a.search_tokens = tokens(&a);
        let mut found = None;
        for r in recent.iter_mut() {
            if r.search_tokens.is_empty() {
                r.search_tokens = tokens(r);
            }
            if urls::same(&a.actual_deal_url, &r.actual_deal_url) || fuzzy(&a, r) {
                found = Some(r.clone());
                break;
            }
        }
        if let Some(r) = found {
            a.document_id = r.document_id.clone();
            existing.entry(r.document_id.clone()).or_insert(r);
            out.push(a);
            continue;
        }
        let mut merged = false;
        for (j, b) in scraped.iter_mut().enumerate().skip(i + 1) {
            if matched.contains(&j) {
                continue;
            }
            b.search_tokens = tokens(b);
            if urls::same(&a.actual_deal_url, &b.actual_deal_url) || fuzzy(&a, b) {
                b.document_id = a.document_id.clone();
                matched.insert(j);
                if !merged {
                    out.push(a.clone());
                    merged = true;
                }
                out.push(b.clone());
            }
        }
        if !merged {
            out.push(a);
        }
    }
    out
}
pub fn by_detailed_url(
    mut deals: Vec<DealInfo>,
    existing: &mut BTreeMap<String, DealInfo>,
    recent: &[DealInfo],
) -> Vec<DealInfo> {
    let index = recent_by_url(recent);
    let mut first: BTreeMap<String, String> = BTreeMap::new();
    for d in &mut deals {
        let key = urls::canonical(&d.actual_deal_url);
        if key.is_empty() {
            continue;
        }
        if let Some(r) = index
            .get(&key)
            .map(|&i| &recent[i])
            .filter(|r| r.document_id != d.document_id)
        {
            d.document_id = r.document_id.clone();
            existing
                .entry(r.document_id.clone())
                .or_insert_with(|| r.clone());
            first.insert(key, r.document_id.clone());
            continue;
        }
        if let Some(id) = first.get(&key)
            && id != &d.document_id
        {
            d.document_id = id.clone();
            continue;
        }
        first.insert(key, d.document_id.clone());
    }
    deals
}
