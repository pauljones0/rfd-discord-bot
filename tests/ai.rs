mod support;
use rfd_bot::{
    ai::{Cleaner, parse_titles},
    control::Control,
    db::Db,
    models::{DealInfo, GeminiQuotaStatus},
    storage::Store,
    time::Timestamp,
};
use serde_json::json;
use std::{
    sync::{
        Arc, Mutex,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};

#[tokio::test(flavor = "current_thread")]
async fn free_provider_rejection_preserves_titles_and_cooldown_across_restart() {
    for status in [401, 403, 429] {
        let (db, worker) = Db::new(Store::open(std::path::Path::new(":memory:")).unwrap()).unwrap();
        let calls = Arc::new(AtomicUsize::new(0));
        let n = calls.clone();
        let source = support::server(move |r| {
            n.fetch_add(1, Ordering::SeqCst);
            let body: serde_json::Value = serde_json::from_slice(&r.body).unwrap();
            assert_eq!(body["generationConfig"]["maxOutputTokens"], 2048);
            (
                status,
                vec![],
                br#"{"error":{"message":"fixture rejection"}}"#.to_vec(),
            )
        })
        .await;
        let (_stop, rx) = tokio::sync::watch::channel(false);
        for _ in 0..2 {
            let cleaner = Cleaner::with_free_base(
                db.clone(),
                "fixture-key".into(),
                "fixture-model".into(),
                source.base.clone(),
            )
            .await
            .unwrap();
            let mut deals = vec![DealInfo {
                title: "Original SSD sale".into(),
                ..Default::default()
            }];
            cleaner
                .clean(
                    &mut deals,
                    &Control::new(Duration::from_secs(5), rx.clone()),
                )
                .await;
            assert_eq!(deals[0].title, "Original SSD sale");
            assert!(!deals[0].ai_processed);
            assert!(deals[0].clean_title.is_empty());
        }
        assert_eq!(calls.load(Ordering::SeqCst), 1);
        let quota = db.call(|s| s.quota()).await.unwrap().unwrap();
        assert!(quota.all_exhausted);
        assert_eq!(quota.daily_requests, 1);
        drop(db);
        worker.join().unwrap();
    }
}

#[tokio::test(flavor = "current_thread")]
async fn free_budget_survives_cooldown_and_blocks_calls_if_reservation_fails() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("free-budget.sqlite");
    let (db, worker) = Db::new(Store::open(&path).unwrap()).unwrap();
    let calls = Arc::new(AtomicUsize::new(0));
    let n = calls.clone();
    let source = support::server(move |_| {
        n.fetch_add(1, Ordering::SeqCst);
        support::json(json!({}))
    })
    .await;
    let quota = GeminiQuotaStatus {
        current_day: chrono::Utc::now()
            .with_timezone(&chrono_tz::America::Los_Angeles)
            .format("%Y-%m-%d")
            .to_string(),
        daily_requests: 20,
        all_exhausted: true,
        exhausted_at: Timestamp::from_datetime(chrono::Utc::now() - chrono::Duration::hours(1)),
        ..Default::default()
    };
    db.call(move |s| s.save_quota(&quota)).await.unwrap();
    let cleaner = Cleaner::with_free_base(
        db.clone(),
        "fixture-key".into(),
        "fixture-model".into(),
        source.base.clone(),
    )
    .await
    .unwrap();
    let (_stop, rx) = tokio::sync::watch::channel(false);
    let mut deals = vec![DealInfo {
        title: "Original".into(),
        ..Default::default()
    }];
    cleaner
        .clean(
            &mut deals,
            &Control::new(Duration::from_secs(5), rx.clone()),
        )
        .await;
    assert_eq!(
        db.call(|s| s.quota())
            .await
            .unwrap()
            .unwrap()
            .daily_requests,
        20
    );
    assert_eq!(calls.load(Ordering::SeqCst), 0);
    drop(cleaner);
    db.call(|s| s.save_quota(&GeminiQuotaStatus::default()))
        .await
        .unwrap();
    let cleaner = Cleaner::with_free_base(
        db.clone(),
        "fixture-key".into(),
        "fixture-model".into(),
        source.base.clone(),
    )
    .await
    .unwrap();
    rusqlite::Connection::open(&path)
        .unwrap()
        .execute_batch("DROP TABLE settings")
        .unwrap();
    cleaner
        .clean(&mut deals, &Control::new(Duration::from_secs(5), rx))
        .await;
    assert_eq!(calls.load(Ordering::SeqCst), 0);
    drop(cleaner);
    drop(db);
    worker.join().unwrap();
}
#[test]
fn parsing_ignores_thoughts_and_repairs_wrapped_split_json() {
    let v = json!({"candidates":[{"content":{"parts":[{"thought":true,"text":"[{\"index\":0,\"clean_title\":\"wrong\"}]"},{"text":"```json\n[{\"index\":0,"},{"text":"\"clean_title\":\"SSD {sale} \\\"one\\\"\"}]\n```"}]}}]});
    let titles = parse_titles(&v).unwrap();
    assert_eq!(titles.len(), 1);
    assert_eq!(titles[0].clean_title, "SSD {sale} \"one\"");
}
#[tokio::test(flavor = "current_thread")]
async fn repairs_missing_indexes_and_preserves_restored_cooldown() {
    let temp = tempfile::tempdir().unwrap();
    let (db, worker) = Db::new(Store::open(&temp.path().join("rfd.sqlite")).unwrap()).unwrap();
    let calls = Arc::new(AtomicUsize::new(0));
    let n = calls.clone();
    let prompts = Arc::new(Mutex::new(Vec::new()));
    let recorded = prompts.clone();
    let source=support::server(move|r|{assert!(r.headers.to_lowercase().contains("x-goog-api-key: fixture-key"));assert_eq!(r.path,"/fixture-model:generateContent");let body:serde_json::Value=serde_json::from_slice(&r.body).unwrap();recorded.lock().unwrap().push(body["contents"][0]["parts"][0]["text"].as_str().unwrap().to_owned());let result=if n.fetch_add(1,Ordering::SeqCst)==0{r#"[{"index":0,"clean_title":"First SSD sale"}]"#}else{r#"[{"index":1,"clean_title":"Second SSD sale"}]"#};support::json(json!({"candidates":[{"content":{"parts":[{"text":result}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}))}).await;
    let cleaner = Cleaner::with_base(
        db.clone(),
        vec!["fixture-key".into()],
        vec!["fixture-model".into()],
        source.base.clone(),
    )
    .await
    .unwrap();
    let (_stop, rx) = tokio::sync::watch::channel(false);
    let mut deals = vec![
        DealInfo {
            title: "first".into(),
            ..Default::default()
        },
        DealInfo {
            title: "second".into(),
            ..Default::default()
        },
    ];
    cleaner
        .clean(
            &mut deals,
            &Control::new(Duration::from_secs(5), rx.clone()),
        )
        .await;
    assert!(deals.iter().all(|d| d.ai_processed));
    assert_eq!(calls.load(Ordering::SeqCst), 2);
    let prompt = prompts.lock().unwrap()[1].clone();
    assert!(prompt.contains("ONLY these missing"));
    assert!(prompt.contains("1. Title"));
    assert!(!prompt.contains("0. Title"));
    drop(cleaner);
    let q = GeminiQuotaStatus {
        current_day: chrono::Utc::now()
            .with_timezone(&chrono_tz::America::Los_Angeles)
            .format("%Y-%m-%d")
            .to_string(),
        current_model: "removed-model".into(),
        current_location: "key9".into(),
        all_exhausted: true,
        exhausted_at: Timestamp::now(),
        ..Default::default()
    };
    db.call(move |s| s.save_quota(&q)).await.unwrap();
    let cleaner = Cleaner::with_base(
        db.clone(),
        vec!["fixture-key".into()],
        vec!["fixture-model".into()],
        source.base.clone(),
    )
    .await
    .unwrap();
    let mut pending = vec![DealInfo {
        title: "pending".into(),
        ..Default::default()
    }];
    cleaner
        .clean(&mut pending, &Control::new(Duration::from_secs(5), rx))
        .await;
    assert!(!pending[0].ai_processed);
    assert_eq!(calls.load(Ordering::SeqCst), 2);
    assert!(db.call(|s| s.quota()).await.unwrap().unwrap().all_exhausted);
    drop(cleaner);
    drop(db);
    worker.join().unwrap();
}
