use rfd_bot::{models::ThreadContext, notifier, parse, reconcile, urls};

const LIST_URL: &str = "https://forums.redflagdeals.com/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d";

fn card(href: &str) -> String {
    format!(
        r#"<li class="topic-card topic"><a class="topic-card-info thread_info" href="{href}"><h3 class="thread_title">Fixture sale</h3><time class="topic_time" datetime="2026-10-01T10:00:00Z"></time></a></li>"#
    )
}

#[test]
fn thread_links_resolve_against_the_listing_url_before_normalization() {
    for (href, expected) in [
        (
            "/fixture-sale-12345/",
            "https://forums.redflagdeals.com/fixture-sale-12345",
        ),
        (
            "../fixture-sale-12345/",
            "https://forums.redflagdeals.com/fixture-sale-12345",
        ),
        (
            "//forums.redflagdeals.com/fixture-sale-12345/",
            "https://forums.redflagdeals.com/fixture-sale-12345",
        ),
        (
            "http://www.redflagdeals.com/fixture-sale-12345/?utm_source=fixture&amp;p=42#p42",
            "https://forums.redflagdeals.com/fixture-sale-12345?p=42#p42",
        ),
    ] {
        let mut deal = parse::list(&card(href), LIST_URL, &parse::Selectors::defaults())
            .unwrap()
            .remove(0);
        parse::assign_id(&mut deal).unwrap();
        assert_eq!(deal.post_url, expected, "{href}");
        assert_eq!(deal.threads[0].post_url, expected);
        assert_eq!(urls::thread_key(&deal.post_url), "rfd:12345");
        let embed = notifier::embed(&deal);
        assert_eq!(embed.url, expected);
        assert!(
            embed
                .description
                .starts_with(&format!("[RFD]({expected}) "))
        );
        deal.actual_deal_url = "https://example.invalid/product".into();
        let embed = notifier::embed(&deal);
        assert_eq!(embed.url, deal.actual_deal_url);
        assert!(
            embed
                .description
                .starts_with(&format!("[RFD]({expected}) "))
        );
    }
}

#[test]
fn missing_or_invalid_thread_href_is_not_promoted_to_the_listing_url() {
    for href in ["", " ", "https://[invalid"] {
        let mut deal = parse::list(&card(href), LIST_URL, &parse::Selectors::defaults())
            .unwrap()
            .remove(0);
        assert!(deal.post_url.is_empty());
        assert!(parse::assign_id(&mut deal).is_err());
    }
}

#[test]
fn observed_thread_repairs_stored_listing_link_without_losing_receipts_or_other_threads() {
    let mut observed = parse::list(
        &card("/fixture-sale-12345/"),
        "https://forums.redflagdeals.com",
        &parse::Selectors::defaults(),
    )
    .unwrap()
    .remove(0);
    parse::assign_id(&mut observed).unwrap();
    let mut stored = observed.clone();
    stored.post_url = "https://forums.redflagdeals.com/hot-deals-f9".into();
    stored.threads[0].post_url = stored.post_url.clone();
    stored.discord_message_ids.insert("42".into(), "942".into());
    stored
        .discord_message_application_ids
        .insert("42".into(), "1001".into());
    let other = ThreadContext {
        document_id: "other-thread".into(),
        post_url: "https://forums.redflagdeals.com/other-sale-23456".into(),
        ..Default::default()
    };
    stored.threads.push(other.clone());
    let repaired = reconcile::reconcile(Some(&stored), &[observed.clone()]).unwrap();
    assert_eq!(repaired.post_url, observed.post_url);
    assert_eq!(repaired.threads.len(), 2);
    assert_eq!(repaired.threads[0], observed.threads[0]);
    assert_eq!(repaired.threads[1], other);
    assert_eq!(repaired.discord_message_ids, stored.discord_message_ids);
    assert_eq!(
        repaired.discord_message_application_ids,
        stored.discord_message_application_ids
    );
    assert!(
        !notifier::embed(&repaired)
            .description
            .contains("hot-deals-f9")
    );
    assert_eq!(
        reconcile::reconcile(Some(&repaired), &[observed]).unwrap(),
        repaired
    );
}

#[test]
fn listing_link_repair_requires_a_matching_nonempty_thread_identity() {
    for identity in ["", "another-identity"] {
        let old = ThreadContext {
            document_id: identity.into(),
            post_url: "https://forums.redflagdeals.com/hot-deals-f9".into(),
            ..Default::default()
        };
        let mut deal = rfd_bot::models::DealInfo {
            threads: vec![old.clone()],
            ..Default::default()
        };
        let new = ThreadContext {
            document_id: "observed-identity".into(),
            post_url: "https://forums.redflagdeals.com/fixture-sale-12345".into(),
            ..Default::default()
        };
        reconcile::merge_thread(&mut deal, &new);
        assert_eq!(deal.threads, [old, new]);
    }
}
