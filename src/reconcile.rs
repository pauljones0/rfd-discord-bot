use crate::{
    models::{DealInfo, ThreadContext},
    urls,
};
use std::collections::{BTreeMap, BTreeSet};
pub fn sort_threads(d: &mut DealInfo) {
    d.threads.sort_by(|a, b| {
        b.like_count
            .cmp(&a.like_count)
            .then_with(|| b.comment_count.cmp(&a.comment_count))
    });
}
fn is_hot_deals_list(raw: &str) -> bool {
    url::Url::parse(raw).is_ok_and(|u| {
        matches!(
            u.host_str(),
            Some("forums.redflagdeals.com" | "redflagdeals.com" | "www.redflagdeals.com")
        ) && u.path().trim_end_matches('/') == "/hot-deals-f9"
    })
}
pub fn merge_thread(d: &mut DealInfo, new: &ThreadContext) {
    if new.not_found {
        return;
    }
    let key = urls::thread_key(&new.post_url);
    if let Some(old) = d.threads.iter_mut().find(|t| {
        urls::thread_key(&t.post_url) == key
                // Older Rust polls appended a relative href to the list query,
                // then removed it as tracking. Repair only an observed identity.
                || (!new.document_id.is_empty()
                    && t.document_id == new.document_id
                    && is_hot_deals_list(&t.post_url)
                    && !is_hot_deals_list(&new.post_url))
    }) {
        old.like_count = new.like_count;
        old.comment_count = new.comment_count;
        old.view_count = if new.view_count_available {
            new.view_count
        } else {
            0
        };
        old.view_count_available = new.view_count_available;
        old.post_url = new.post_url.clone();
    } else {
        d.threads.push(new.clone());
    }
}
pub fn fill_missing(base: &mut DealInfo, candidate: &DealInfo) {
    macro_rules! fill{($($f:ident),*)=>{$(if base.$f.is_empty(){base.$f=candidate.$f.clone();})*};}
    fill!(
        post_url,
        retailer,
        category,
        price,
        original_price,
        savings,
        actual_deal_url,
        thread_image_url,
        description,
        comments,
        summary,
        search_tokens
    );
}
pub fn preserve_details(base: &mut DealInfo, existing: &DealInfo) {
    if !existing.actual_deal_url.is_empty()
        && urls::same(&existing.actual_deal_url, &base.actual_deal_url)
    {
        base.actual_deal_url = existing.actual_deal_url.clone();
    }
    fill_missing(base, existing);
}
pub fn live(observations: &[DealInfo]) -> Vec<DealInfo> {
    observations
        .iter()
        .filter_map(|d| {
            if d.threads.is_empty() {
                return Some(d.clone());
            }
            let mut new = d.clone();
            new.threads.retain(|t| !t.not_found);
            if new.threads.is_empty() {
                return None;
            }
            if urls::thread_key(primary_url(d)) != urls::thread_key(&new.threads[0].post_url) {
                new.post_url = new.threads[0].post_url.clone();
            }
            Some(new)
        })
        .collect()
}
pub fn primary_url(d: &DealInfo) -> &str {
    d.threads
        .first()
        .map(|t| t.post_url.as_str())
        .unwrap_or(&d.post_url)
}
fn dedup_threads(d: &mut DealInfo) {
    let mut seen: BTreeMap<String, usize> = BTreeMap::new();
    let mut out: Vec<ThreadContext> = Vec::new();
    for t in &d.threads {
        let key = urls::thread_key(&t.post_url);
        if let Some(i) = seen.get(&key) {
            if t.like_count > out[*i].like_count {
                out[*i] = t.clone();
            }
        } else {
            seen.insert(key, out.len());
            out.push(t.clone());
        }
    }
    d.threads = out;
}
fn content_base(existing: &DealInfo, scraped: &[DealInfo]) -> DealInfo {
    if scraped.is_empty() {
        return existing.clone();
    }
    let mut keys: BTreeSet<_> = existing
        .threads
        .iter()
        .filter(|t| !existing.document_id.is_empty() && t.document_id == existing.document_id)
        .map(|t| urls::thread_key(&t.post_url))
        .filter(|k| !k.is_empty())
        .collect();
    if keys.is_empty() {
        let url = if existing.post_url.is_empty() {
            primary_url(existing)
        } else {
            &existing.post_url
        };
        let key = urls::thread_key(url);
        if !key.is_empty() {
            keys.insert(key);
        }
    }
    let same_thread = scraped.iter().find(|d| {
        d.threads
            .iter()
            .any(|t| keys.contains(&urls::thread_key(&t.post_url)))
    });
    let same_document = scraped.iter().find(|d| {
        d.document_id == existing.document_id
            && !d
                .threads
                .iter()
                .any(|t| !t.document_id.is_empty() && t.document_id != existing.document_id)
    });
    if let Some(source) = same_thread.or(same_document) {
        let mut base = source.clone();
        preserve_details(&mut base, existing);
        return base;
    }
    let mut base = existing.clone();
    for d in scraped {
        fill_missing(&mut base, d);
    }
    base
}
pub fn reconcile(existing: Option<&DealInfo>, observations: &[DealInfo]) -> Option<DealInfo> {
    let live = live(observations);
    let Some(existing) = existing else {
        let mut d = live.first()?.clone();
        for duplicate in &live[1..] {
            fill_missing(&mut d, duplicate);
            for thread in &duplicate.threads {
                merge_thread(&mut d, thread);
            }
        }
        sort_threads(&mut d);
        return Some(d);
    };
    let mut d = existing.clone();
    dedup_threads(&mut d);
    let missing: BTreeSet<_> = observations
        .iter()
        .flat_map(|d| &d.threads)
        .filter(|t| t.not_found)
        .map(|t| urls::thread_key(&t.post_url))
        .filter(|k| !k.is_empty())
        .collect();
    let prior = d.threads.len();
    d.threads
        .retain(|t| !missing.contains(&urls::thread_key(&t.post_url)));
    let removed = d.threads.len() != prior;
    let base = content_base(&d, &live);
    for observation in &live {
        for thread in &observation.threads {
            merge_thread(&mut d, thread);
        }
    }
    if d.title != base.title {
        d.clean_title.clear();
        d.ai_processed = false;
    }
    macro_rules! copy{($($f:ident),*)=>{$(d.$f=base.$f.clone();)*};}
    copy!(
        title,
        post_url,
        retailer,
        category,
        price,
        original_price,
        savings,
        thread_image_url,
        published_timestamp,
        actual_deal_url,
        description,
        comments,
        summary,
        search_tokens
    );
    d.category = crate::category::normalize(&d.category);
    if base.ai_processed && !base.clean_title.is_empty() {
        d.clean_title = base.clean_title;
        d.ai_processed = true;
    }
    sort_threads(&mut d);
    if removed {
        d.post_url = d
            .threads
            .first()
            .map(|t| t.post_url.clone())
            .unwrap_or_default();
    }
    Some(d)
}
