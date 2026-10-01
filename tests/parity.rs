use anyhow::Result;
use rfd_bot::parse::{self, Selectors};
use serde_json::Value;
#[test]
fn scraper_matches_go_output() -> Result<()> {
    let cases: Vec<Value> = serde_json::from_str(include_str!("fixtures/go-rfd-parse.json"))?;
    for case in cases {
        let s = Selectors::defaults();
        let html = case["html"].as_str().unwrap();
        if case["kind"] == "list" {
            // Original unit snippets select li.topic; use that exact selector here.
            let mut config: Value =
                serde_json::from_slice(include_bytes!("../src/selectors.json"))?;
            config["hot_deals_list"]["container"]["item"] = serde_json::json!("li.topic");
            let s = Selectors::from_bytes(&serde_json::to_vec(&config)?)?;
            let d = parse::list(html, "https://forums.redflagdeals.com", &s)?.remove(0);
            let expected: rfd_bot::models::DealInfo =
                serde_json::from_value(case["expected"].clone())?;
            assert_eq!(d, expected, "{}", case["name"]);
        } else {
            assert_eq!(
                serde_json::to_value(parse::detail(html, &s)?)?,
                case["expected"],
                "{}",
                case["name"]
            );
        }
    }
    Ok(())
}
#[test]
fn identities_preserve_timezone_and_nanosecond_normalization() -> Result<()> {
    let html = r#"<li class="topic-card topic"><a class="topic-card-info thread_info" href="/fixture"><span class="thread_title">Fixture sale</span></a><time class="topic_time" datetime="2026-09-29T12:00:00.123400000-06:00"></time></li>"#;
    let mut d = parse::list(
        html,
        "https://forums.redflagdeals.com",
        &Selectors::defaults(),
    )?
    .remove(0);
    parse::assign_id(&mut d)?;
    assert_eq!(d.published_timestamp.0, "2026-09-29T12:00:00.1234-06:00");
    use sha2::{Digest, Sha256};
    assert_eq!(
        d.document_id,
        hex::encode(Sha256::digest(b"2026-09-29T12:00:00.1234-06:00"))
    );
    Ok(())
}
#[test]
fn product_and_referral_links_match_go_corpus() -> Result<()> {
    let cases: Vec<Value> = serde_json::from_str(include_str!("fixtures/go-rfd-urls.json"))?;
    for c in cases {
        let url = c["url"].as_str().unwrap();
        assert_eq!(
            rfd_bot::urls::clean_product(url),
            c["product"].as_str().unwrap(),
            "product {url}"
        );
        assert_eq!(
            rfd_bot::urls::referral(
                url,
                c["tag"].as_str().unwrap(),
                c["prefix"].as_str().unwrap()
            ),
            c["referral"].as_str().unwrap(),
            "referral {url}"
        );
    }
    Ok(())
}
#[test]
fn dedupe_reconciliation_and_quality_match_240_go_cases() -> Result<()> {
    use rfd_bot::{
        dedupe,
        models::{DealInfo, Subscription},
        quality, reconcile, urls,
    };
    let cases: Vec<Value> = serde_json::from_str(include_str!("fixtures/go-rfd-reconcile.json"))?;
    for (i, c) in cases.into_iter().enumerate() {
        let existing: Option<DealInfo> = serde_json::from_value(c["existing"].clone())?;
        let observations: Vec<DealInfo> = serde_json::from_value(c["observations"].clone())?;
        let expected: Option<DealInfo> = serde_json::from_value(c["reconciled"].clone())?;
        assert_eq!(
            reconcile::reconcile(existing.as_ref(), &observations),
            expected,
            "reconcile case {i}"
        );
        let d: DealInfo = serde_json::from_value(c["deal"].clone())?;
        assert_eq!(
            numeric(serde_json::to_value(quality::discount(&d))?),
            numeric(c["discount"].clone()),
            "discount {i}"
        );
        let mut heated = d.clone();
        quality::apply(&mut heated);
        let expected: DealInfo = serde_json::from_value(c["heated"].clone())?;
        assert_eq!(heated, expected, "heat case {i}");
        for (kind, expected) in c["eligible"].as_object().unwrap() {
            assert_eq!(
                quality::eligible(
                    &d,
                    &Subscription {
                        deal_type: kind.clone(),
                        ..Default::default()
                    }
                ),
                expected.as_bool().unwrap(),
                "eligible case {i}, {kind}"
            );
        }
        assert_eq!(
            serde_json::to_value(dedupe::tokens(&d))?,
            c["tokens"],
            "tokens {i}"
        );
        assert_eq!(
            urls::canonical(&d.actual_deal_url),
            c["canonical_url"].as_str().unwrap()
        );
        let mut recent: Vec<DealInfo> = serde_json::from_value(c["recent"].clone())?;
        let mut map = serde_json::from_value(c["dedupe_existing"].clone())?;
        let deduped = dedupe::deduplicate(observations, &mut map, &mut recent);
        let deduped = dedupe::by_detailed_url(deduped, &mut map, &recent);
        let expected: Vec<DealInfo> = serde_json::from_value(c["deduped"].clone())?;
        assert_eq!(deduped, expected, "dedupe {i}");
    }
    Ok(())
}
fn numeric(v: Value) -> Value {
    match v {
        Value::Number(n) => serde_json::json!(n.as_f64().unwrap()),
        Value::Array(a) => Value::Array(a.into_iter().map(numeric).collect()),
        Value::Object(o) => Value::Object(o.into_iter().map(|(k, v)| (k, numeric(v))).collect()),
        v => v,
    }
}
