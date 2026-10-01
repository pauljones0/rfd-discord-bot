mod support;
use rfd_bot::{
    client::Client, config::Config, control::Control, db::Db, discord::Discord,
    models::Subscription, parse::Selectors, processor::Processor, storage::Store, time::Timestamp,
};
use serde_json::json;
use std::{
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};
fn config(path: std::path::PathBuf) -> Config {
    Config {
        token: "fixture-only".into(),
        app_id: "1001".into(),
        guild_id: String::new(),
        database: path,
        listen: "127.0.0.1:0".into(),
        poll_interval: Duration::from_secs(60),
        poll_timeout: Duration::from_secs(15),
        update_interval: Duration::from_secs(600),
        max_deals: 2000,
        amazon_tag: String::new(),
        bestbuy_prefix: String::new(),
        gemini_keys: vec![],
        gemini_models: vec![],
    }
}
async fn source() -> support::Server {
    let published = Timestamp::now().0;
    support::server(move|r|if r.path.starts_with("/hot-deals"){(200,vec![],format!("<li class='topic-card topic'><a class='topic-card-info thread_info' href='/sale-12345'><h3 class='thread_title'>SSD 1TB sale 20% off</h3><time class='topic_time' datetime='{published}'></time></a><div class='thread_extra_info'><span class='votes'>42</span><span class='posts'>10</span></div></li>").into_bytes())}else{(200,vec![],br#"<div class='deal_link'><a href='https://example.invalid/product'>Buy</a></div><script type='application/ld+json'>{"@type":"DiscussionForumPosting","text":"Fixture description"}</script>"#.to_vec())}).await
}
#[tokio::test(flavor = "current_thread")]
async fn full_polls_retry_missing_receipts_and_preserve_completed_channels() {
    full_poll_receipts(false).await;
}
#[tokio::test(flavor = "current_thread")]
async fn batched_cloud_polls_keep_receipts_and_retry_only_missing_channels() {
    full_poll_receipts(true).await;
}
#[tokio::test(flavor = "current_thread")]
async fn invalid_free_gemini_key_still_delivers_and_preserves_receipts() {
    let temp = tempfile::tempdir().unwrap();
    let config = config(temp.path().join("fallback.sqlite"));
    let mut store = Store::open(&config.database).unwrap();
    store.bind_application(&config.app_id).unwrap();
    store
        .save_subscription(&Subscription {
            guild_id: "1".into(),
            channel_id: "42".into(),
            deal_type: "rfd_all".into(),
            subscription_type: "rfd".into(),
            added_at: Timestamp::now(),
            ..Default::default()
        })
        .unwrap();
    let (db, worker) = Db::new(store).unwrap();
    let source = source().await;
    let ai = support::server(|_| {
        (
            403,
            vec![],
            br#"{"error":{"message":"fixture invalid key"}}"#.to_vec(),
        )
    })
    .await;
    let discord = support::server(|r| {
        let body: serde_json::Value = serde_json::from_slice(&r.body).unwrap();
        assert!(body["embeds"][0]["title"].as_str().unwrap().contains("SSD"));
        support::json(json!({"id":"942","channel_id":"42"}))
    })
    .await;
    let cleaner = rfd_bot::ai::Cleaner::with_free_base(
        db.clone(),
        "fixture-key".into(),
        "fixture-model".into(),
        ai.base.clone(),
    )
    .await
    .unwrap();
    let processor = Processor::new(
        db.clone(),
        Client::with_source(
            Selectors::defaults(),
            String::new(),
            String::new(),
            format!("{}/hot-deals", source.base),
            vec!["127.0.0.1".into()],
        )
        .unwrap(),
        Discord::with_base(
            config.token.clone(),
            config.app_id.clone(),
            discord.base.clone(),
        )
        .unwrap(),
        config,
    )
    .with_cleaner(Some(cleaner))
    .with_batch_persistence();
    let (_stop, rx) = tokio::sync::watch::channel(false);
    assert_eq!(
        processor
            .process(Control::new(Duration::from_secs(15), rx.clone()))
            .await
            .unwrap()
            .sent,
        1
    );
    assert_eq!(
        processor
            .process(Control::new(Duration::from_secs(15), rx))
            .await
            .unwrap()
            .sent,
        0
    );
    let deals = db.call(|s| s.recent(i64::MIN)).await.unwrap();
    assert_eq!(deals[0].discord_message_ids["42"], "942");
    assert!(!deals[0].ai_processed);
    assert_eq!(
        db.call(|s| s.quota())
            .await
            .unwrap()
            .unwrap()
            .daily_requests,
        1
    );
    drop(processor);
    drop(db);
    worker.join().unwrap();
}
async fn full_poll_receipts(batch: bool) {
    let temp = tempfile::tempdir().unwrap();
    let config = config(temp.path().join("rfd.sqlite"));
    let mut store = Store::open(&config.database).unwrap();
    store.bind_application(&config.app_id).unwrap();
    for channel in ["42", "43"] {
        store
            .save_subscription(&Subscription {
                guild_id: "1".into(),
                channel_id: channel.into(),
                deal_type: "rfd_all".into(),
                subscription_type: "rfd".into(),
                added_at: Timestamp::now(),
                ..Default::default()
            })
            .unwrap();
    }
    let (db, worker) = Db::new(store).unwrap();
    let source = source().await;
    let posts = Arc::new(AtomicUsize::new(0));
    let count = posts.clone();
    let failures = Arc::new(AtomicUsize::new(0));
    let failed = failures.clone();
    let discord = support::server(move |r| {
        assert_eq!(r.method, "POST");
        count.fetch_add(1, Ordering::SeqCst);
        if r.path.contains("/43/") && failed.fetch_add(1, Ordering::SeqCst) == 0 {
            (400, vec![], vec![])
        } else {
            let channel = r.path.split('/').nth(2).unwrap();
            support::json(json!({"id":format!("9{channel}"),"channel_id":channel}))
        }
    })
    .await;
    let processor = Processor::new(
        db.clone(),
        Client::with_source(
            Selectors::defaults(),
            String::new(),
            String::new(),
            format!("{}/hot-deals", source.base),
            vec!["127.0.0.1".into()],
        )
        .unwrap(),
        Discord::with_base(
            config.token.clone(),
            config.app_id.clone(),
            discord.base.clone(),
        )
        .unwrap(),
        config,
    );
    let processor = if batch {
        processor.with_batch_persistence()
    } else {
        processor
    };
    let (_stop, rx) = tokio::sync::watch::channel(false);
    assert!(
        processor
            .process(Control::new(Duration::from_secs(15), rx.clone()))
            .await
            .is_err()
    );
    let saved = db.call(|s| s.recent(i64::MIN)).await.unwrap();
    assert_eq!(saved.len(), 1);
    assert_eq!(saved[0].discord_message_ids.len(), 1);
    let metrics = processor
        .process(Control::new(Duration::from_secs(15), rx.clone()))
        .await
        .unwrap();
    assert_eq!(metrics.sent, 1);
    assert_eq!(posts.load(Ordering::SeqCst), 3);
    assert_eq!(
        processor
            .process(Control::new(Duration::from_secs(15), rx))
            .await
            .unwrap()
            .sent,
        0
    );
    assert_eq!(posts.load(Ordering::SeqCst), 3);
    let path = temp.path().join("rfd.sqlite");
    drop(processor);
    drop(db);
    worker.join().unwrap();
    let store = Store::open(&path).unwrap();
    let saved = store.recent(i64::MIN).unwrap();
    assert_eq!(saved[0].discord_message_ids.len(), 2);
    assert_eq!(saved[0].discord_message_application_ids["42"], "1001");
}
#[tokio::test(flavor = "current_thread")]
async fn shutdown_during_database_wait_keeps_acknowledged_receipt() {
    let temp = tempfile::tempdir().unwrap();
    let config = config(temp.path().join("rfd.sqlite"));
    let mut store = Store::open(&config.database).unwrap();
    store.bind_application(&config.app_id).unwrap();
    store
        .save_subscription(&Subscription {
            guild_id: "1".into(),
            channel_id: "42".into(),
            deal_type: "rfd_all".into(),
            subscription_type: "rfd".into(),
            added_at: Timestamp::now(),
            ..Default::default()
        })
        .unwrap();
    let (db, worker) = Db::new(store).unwrap();
    let source = source().await;
    let blocker = Arc::new(std::sync::Mutex::new(
        rusqlite::Connection::open(&config.database).unwrap(),
    ));
    let held = blocker.clone();
    let (sent, mut notification) = tokio::sync::mpsc::channel(1);
    let discord = support::server(move |_| {
        held.lock()
            .unwrap()
            .execute_batch("BEGIN IMMEDIATE")
            .unwrap();
        sent.try_send(()).unwrap();
        support::json(json!({"id":"942","channel_id":"42"}))
    })
    .await;
    let processor = Arc::new(Processor::new(
        db.clone(),
        Client::with_source(
            Selectors::defaults(),
            String::new(),
            String::new(),
            format!("{}/hot-deals", source.base),
            vec!["127.0.0.1".into()],
        )
        .unwrap(),
        Discord::with_base(
            config.token.clone(),
            config.app_id.clone(),
            discord.base.clone(),
        )
        .unwrap(),
        config,
    ));
    let (stop, rx) = tokio::sync::watch::channel(false);
    let p = processor.clone();
    let poll =
        tokio::spawn(async move { p.process(Control::new(Duration::from_secs(15), rx)).await });
    tokio::time::timeout(Duration::from_secs(3), notification.recv())
        .await
        .unwrap()
        .unwrap();
    tokio::time::sleep(Duration::from_millis(100)).await;
    stop.send(true).unwrap();
    blocker.lock().unwrap().execute_batch("COMMIT").unwrap();
    assert!(poll.await.unwrap().is_err());
    let saved = db.call(|s| s.recent(i64::MIN)).await.unwrap();
    assert_eq!(saved.len(), 1);
    assert_eq!(saved[0].discord_message_ids["42"], "942");
    drop(processor);
    drop(db);
    worker.join().unwrap();
}
