use anyhow::{Result, ensure};
use rfd_bot::{
    commands,
    config::Config,
    control::Control,
    db::Db,
    discord::{Discord, Gateway},
    models::Subscription,
    storage::Store,
    time::Timestamp,
};
use serde_json::json;
use std::{
    io::{BufRead, Write},
    time::Duration,
};
fn main() -> Result<()> {
    rfd_bot::runtime::executor()?.block_on(run())
}
async fn run() -> Result<()> {
    let args: Vec<_> = std::env::args().collect();
    let base = &args[1];
    ensure!(
        base.starts_with("http://127.0.0.1:"),
        "fixture must use loopback HTTP"
    );
    let path = std::path::PathBuf::from(&args[2]);
    let mut cfg = Config::load()?;
    cfg.database = path.clone();
    let mut store = Store::open(&path)?;
    store.bind_application("1001")?;
    let deals: Vec<rfd_bot::models::DealInfo> = serde_json::from_slice(&std::fs::read(&args[3])?)?;
    store.batch_write(&deals, &[])?;
    store.save_subscription(&Subscription {
        guild_id: "1".into(),
        channel_id: "42".into(),
        subscription_type: "rfd".into(),
        deal_type: "rfd_all".into(),
        added_at: Timestamp::now(),
        ..Default::default()
    })?;
    let (db, worker) = Db::new(store)?;
    let api = Discord::with_base("fixture-only".into(), "1001".into(), base.clone())?;
    let processor = rfd_bot::processor::Processor::new(
        db.clone(),
        rfd_bot::client::Client::with_source(
            rfd_bot::parse::Selectors::defaults(),
            String::new(),
            String::new(),
            format!("{base}/hot-deals"),
            vec!["127.0.0.1".into()],
        )?,
        api.clone(),
        cfg,
    );
    let gateway = std::sync::Arc::new(Gateway::new(api, commands::handler(db.clone())));
    let health = gateway.health.clone();
    let (stop, rx) = tokio::sync::watch::channel(false);
    let g = gateway.clone();
    let r = rx.clone();
    let task = tokio::spawn(async move { g.run(r).await });
    tokio::time::timeout(Duration::from_secs(15), async {
        while !health.ready.load(std::sync::atomic::Ordering::Relaxed) {
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await?;
    for _ in 0..3 {
        processor
            .process(Control::new(Duration::from_secs(30), rx.clone()))
            .await?;
    }
    println!("{}", json!({"phase":"ready"}));
    std::io::stdout().flush()?;
    let (tx, mut requests) = tokio::sync::mpsc::channel(1);
    let reader = std::thread::spawn(move || {
        for line in std::io::stdin().lock().lines() {
            let Ok(line) = line else { break };
            if tx.blocking_send(line).is_err() {
                break;
            }
        }
    });
    while let Some(line) = requests.recv().await {
        let command: serde_json::Value = serde_json::from_str(&line)?;
        for _ in 0..command["polls"].as_u64().unwrap_or(0) {
            processor
                .process(Control::new(Duration::from_secs(30), rx.clone()))
                .await?;
        }
        ensure!(
            db.call(|s| s.recent(i64::MIN)).await?.len() == 2000,
            "wrong deal count"
        );
        println!("{}", json!({"phase":"done"}));
        std::io::stdout().flush()?;
    }
    stop.send(true)?;
    task.await??;
    reader.join().expect("fixture reader");
    drop(gateway);
    drop(processor);
    drop(db);
    worker.join().expect("database worker");
    Ok(())
}
