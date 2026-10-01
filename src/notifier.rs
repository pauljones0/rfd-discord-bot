use crate::{
    category,
    message::{self, Embed, Footer, Thumbnail},
    models::DealInfo,
    quality,
};
use serde_json::Value;
pub fn embed(deal: &DealInfo) -> Embed {
    let mut title = if deal.clean_title.is_empty() {
        deal.title.clone()
    } else {
        deal.clean_title.clone()
    };
    let backed = quality::discount(deal).eligible;
    if backed && deal.has_been_hot {
        title.push_str(" 🔥");
    }
    let mut description = String::new();
    for t in &deal.threads {
        description.push_str(&format!("[RFD]({}) ", t.post_url));
    }
    description.push_str("\n\n");
    let (likes, comments, views, known) = deal
        .threads
        .first()
        .map(|t| {
            (
                t.like_count,
                t.comment_count,
                t.view_count,
                t.view_count_available,
            )
        })
        .unwrap_or_default();
    description.push_str(&format!(
        "{} {likes}  💬 {comments}",
        if likes < 0 { "👎" } else { "👍" }
    ));
    if known {
        description.push_str(&format!("  👀 {views}"));
    }
    let actual = message::valid_url(&deal.actual_deal_url);
    let url = if actual.is_empty() {
        message::valid_url(&deal.post_url)
    } else {
        actual
    };
    let timestamp = if deal.published_timestamp.is_zero() {
        String::new()
    } else {
        chrono::DateTime::parse_from_rfc3339(&deal.published_timestamp.0)
            .map(|t| t.to_rfc3339_opts(chrono::SecondsFormat::Secs, true))
            .unwrap_or_default()
    };
    let footer = if deal.category.is_empty() && deal.retailer.is_empty() {
        String::new()
    } else {
        format!(
            "{} {}",
            if deal.category.is_empty() {
                ""
            } else {
                category::emoji(&deal.category)
            },
            deal.retailer
        )
        .trim()
        .to_owned()
    };
    let color = if backed && (deal.has_been_hot || quality::engaged(deal, 0.20, 40)) {
        message::HOT
    } else if backed && (deal.has_been_warm || quality::engaged(deal, 0.05, 15)) {
        message::WARM
    } else {
        message::COLD
    };
    let mut e = Embed {
        title,
        description,
        url,
        timestamp,
        color,
        thumbnail: Thumbnail {
            url: message::valid_url(&deal.thread_image_url),
        },
        footer: Footer { text: footer },
        ..Default::default()
    };
    e.bound();
    e
}
pub fn payload(deal: &DealInfo) -> Value {
    message::payload(&[embed(deal)], None)
}
pub fn delivery(deal: &DealInfo, app: &str, channel: &str) -> Value {
    let identity = if !deal.document_id.is_empty() {
        &deal.document_id
    } else if !deal.post_url.is_empty() {
        &deal.post_url
    } else {
        &deal.title
    };
    let nonce = message::nonce(&format!("{app}\0{channel}\0{identity}"));
    message::payload(&[embed(deal)], Some(&nonce))
}
