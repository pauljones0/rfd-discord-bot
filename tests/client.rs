mod support;
use rfd_bot::{
    client::{Challenge, Client},
    parse::Selectors,
};
use std::{
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};

#[tokio::test(flavor = "current_thread")]
async fn listing_query_does_not_leak_into_detail_fetches_or_discord_links() {
    let paths = Arc::new(std::sync::Mutex::new(Vec::new()));
    let requests = paths.clone();
    let server = support::server(move |r| {
        requests.lock().unwrap().push(r.path.clone());
        match r.path.as_str() {
            "/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d" => (200, vec![], br#"
                <li class="topic-card topic"><a class="topic-card-info thread_info" href="/with-product-12345"><h3 class="thread_title">Product sale</h3><time class="topic_time" datetime="2026-10-01T10:00:00Z"></time></a></li>
                <li class="topic-card topic"><a class="topic-card-info thread_info" href="/without-product-23456"><h3 class="thread_title">Thread sale</h3><time class="topic_time" datetime="2026-10-01T11:00:00Z"></time></a></li>"#.to_vec()),
            "/with-product-12345" => (200, vec![], br#"<div class="deal_link"><a href="https://example.invalid/product">Buy</a></div>"#.to_vec()),
            "/without-product-23456" => (200, vec![], b"<p>No product link</p>".to_vec()),
            _ => (404, vec![], vec![]),
        }
    }).await;
    let client = Client::with_source(
        Selectors::defaults(),
        String::new(),
        String::new(),
        format!("{}/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d", server.base),
        vec!["127.0.0.1".into()],
    )
    .unwrap();
    let mut deals = client.list().await.unwrap();
    for deal in &mut deals {
        rfd_bot::parse::assign_id(deal).unwrap();
    }
    let (deals, stats) = client.details(deals).await;
    assert_eq!(stats.succeeded, 2);
    assert_eq!(stats.not_found, 0);
    assert_eq!(stats.failed, 0);
    assert_eq!(deals[0].actual_deal_url, "https://example.invalid/product");
    assert!(deals[1].actual_deal_url.is_empty());
    for (deal, path) in deals
        .iter()
        .zip(["/with-product-12345", "/without-product-23456"])
    {
        let thread = format!("{}{path}", server.base);
        let payload = rfd_bot::notifier::payload(deal);
        assert_eq!(
            payload["embeds"][0]["url"].as_str().unwrap(),
            if deal.actual_deal_url.is_empty() {
                thread.as_str()
            } else {
                deal.actual_deal_url.as_str()
            }
        );
        assert!(
            payload["embeds"][0]["description"]
                .as_str()
                .unwrap()
                .starts_with(&format!("[RFD]({thread}) "))
        );
    }
    let mut paths = paths.lock().unwrap().clone();
    paths.sort();
    assert_eq!(
        paths,
        [
            "/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d",
            "/with-product-12345",
            "/without-product-23456"
        ]
    );
}

#[tokio::test(flavor = "current_thread")]
async fn source_session_proof_of_work_body_limits_and_host_guard() {
    let count = Arc::new(AtomicUsize::new(0));
    let calls = count.clone();
    let server=support::server(move|r|{calls.fetch_add(1,Ordering::Relaxed);if !r.headers.to_lowercase().contains("cookie: pow_bypass="){(202,vec![],b"POW_CHALLENGE_DATA={challenge_nonce:'fixture',challenge_hmac:'test',difficulty:'1',difficulty_char:'0',issued_at:'123',cookie_duration:'60'}".to_vec())}else{(200,vec![],include_bytes!("../testdata/mock_snippets.html").to_vec())}}).await;
    let client = Client::with_source(
        Selectors::defaults(),
        String::new(),
        String::new(),
        format!("{}/list", server.base),
        vec!["127.0.0.1".into()],
    )
    .unwrap();
    assert!(!client.list().await.unwrap().is_empty());
    assert!(!client.list().await.unwrap().is_empty());
    assert_eq!(count.load(Ordering::Relaxed), 3);
    let deal = rfd_bot::models::DealInfo {
        post_url: "https://example.invalid/blocked".into(),
        ..Default::default()
    };
    let (_, stats) = client.details(vec![deal]).await;
    assert_eq!(stats.failed, 1);
    assert_eq!(count.load(Ordering::Relaxed), 3);
    let challenge=Challenge::parse("POW_CHALLENGE_DATA={challenge_nonce:'fixture',challenge_hmac:'test',difficulty:'1',difficulty_char:'a',issued_at:'123'}").unwrap();
    let (cookie, n) = challenge.solve().await.unwrap();
    let parts: Vec<_> = cookie.split('|').collect();
    assert_eq!(parts[2], n.to_string());
    assert!(parts[3].starts_with('a'));
    use sha2::Digest;
    assert_eq!(
        parts[3],
        hex::encode(sha2::Sha256::digest(format!("fixture123{n}")))
    );
    assert!(Challenge::parse("POW_CHALLENGE_DATA={challenge_nonce:'bad|nonce',challenge_hmac:'test',difficulty:'1',difficulty_char:'a',issued_at:'123'}").is_err());
    assert!(Challenge::parse("POW_CHALLENGE_DATA={challenge_nonce:'x',challenge_hmac:'test',difficulty:'9',difficulty_char:'a',issued_at:'123'}").is_err());
    let response = support::server(|_| (200, vec![], vec![b'x'; 5 * 1024 * 1024 + 1])).await;
    let http = rfd_bot::http::client(Duration::from_secs(2), false, None).unwrap();
    assert!(
        rfd_bot::http::body(
            http.get(&response.base).send().await.unwrap(),
            5 * 1024 * 1024
        )
        .await
        .is_err()
    );
}
