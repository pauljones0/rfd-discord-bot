use rfd_bot::{category, models::DealInfo, notifier, parse, reconcile};

#[test]
fn responsive_category_markup_reaches_the_embed_once() {
    let detail = parse::detail(
        include_str!("fixtures/rfd-category-duplicate.html"),
        &parse::Selectors::defaults(),
    )
    .unwrap();
    assert_eq!(detail.category, "Home & Garden");
    let deal = DealInfo {
        category: detail.category,
        retailer: "Fixture Store".into(),
        ..Default::default()
    };
    assert_eq!(notifier::embed(&deal).footer.text, "🏡 Fixture Store");
}

#[test]
fn category_selection_skips_empty_nodes_and_keeps_one_label() {
    let html = r#"<div class="thread_category">Category: </div>
        <div class="thread_category">Category: Computers &amp;
            Electronics</div>
        <div class="thread_category">Computers &amp; Electronics</div>"#;
    let detail = parse::detail(html, &parse::Selectors::defaults()).unwrap();
    assert_eq!(detail.category, "Computers & Electronics");
    assert_eq!(category::emoji(&detail.category), "💻");
    let fallback = parse::detail(
        "<dl><dt>Category:</dt><dd>Home &amp;\n Garden</dd></dl>",
        &parse::Selectors::defaults(),
    )
    .unwrap();
    assert_eq!(fallback.category, "Home & Garden");
}

#[test]
fn previously_stored_duplicate_categories_render_and_reconcile_correctly() {
    for (label, emoji) in [
        ("Home & Garden", "🏡"),
        ("Computers & Electronics", "💻"),
        ("Restaurants", "🍔"),
        ("Other", "🏷️"),
    ] {
        for duplicate in [format!("{label}{label}"), format!("{label} {label}")] {
            assert_eq!(category::normalize(&duplicate), label);
            let mut stored = DealInfo {
                document_id: "fixture".into(),
                title: "Fixture deal".into(),
                post_url: "https://forums.redflagdeals.com/fixture-12345/".into(),
                category: duplicate,
                retailer: "Fixture Store".into(),
                ..Default::default()
            };
            stored
                .discord_message_ids
                .insert("42".into(), "1001".into());
            assert_eq!(
                notifier::embed(&stored).footer.text,
                format!("{emoji} Fixture Store")
            );
            let observed = DealInfo {
                category: String::new(),
                ..stored.clone()
            };
            let repaired = reconcile::reconcile(Some(&stored), &[observed]).unwrap();
            assert_eq!(repaired.category, label);
            assert_eq!(repaired.discord_message_ids, stored.discord_message_ids);
        }
    }
    assert_eq!(category::normalize("UnknownUnknown"), "UnknownUnknown");
    assert_eq!(category::emoji("Unknown"), "❌");
}
