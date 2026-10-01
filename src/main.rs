use anyhow::{Result, ensure};
use futures_util::FutureExt;
use rfd_bot::{
    ai::Cleaner,
    client::Client,
    commands,
    config::Config,
    control::Control,
    db::Db,
    discord::{Discord, Gateway},
    env,
    migration::Migration,
    parse::Selectors,
    processor::Processor,
    runtime::{self, Health, Job},
    server::{self, Handler},
    storage::Store,
};
use std::{io::Read, path::PathBuf, sync::Arc, time::Duration};
use tokio::{net::TcpListener, sync::watch, task::JoinSet};
fn main() {
    runtime::start_logging();
    let result = runtime::executor().and_then(|rt| rt.block_on(run()));
    if let Err(e) = result {
        tracing::error!(error=%e,"RFD bot stopped");
        std::process::exit(1);
    }
}
async fn run() -> Result<()> {
    let _ = dotenvy::dotenv();
    let args: Vec<_> = std::env::args().skip(1).collect();
    let mode = args.first().map(String::as_str).unwrap_or("run");
    if matches!(mode, "version" | "--version" | "-version") {
        println!("rfd-bot {} (Rust)", env!("CARGO_PKG_VERSION"));
        return Ok(());
    }
    if mode == "healthcheck" {
        return server::healthcheck(&env::value("LISTEN_ADDR", "127.0.0.1:8080")).await;
    }
    if mode == "import" {
        return import(&args[1..]);
    }
    if mode == "backup" {
        return rfd_bot::backup::command(&args[1..], "data/rfd.sqlite");
    }
    if mode == "check-storage" {
        let store = Store::open(&PathBuf::from(env::value("SQLITE_PATH", "data/rfd.sqlite")))?;
        store.integrity_check()?;
        println!("SQLite storage is ready.");
        return Ok(());
    }
    #[cfg(feature = "gcp")]
    if mode == "cloud-run" {
        return rfd_bot::gcp::run().await;
    }
    let config = Config::load()?;
    if mode == "check-config" {
        println!("RFD configuration is valid (credentials not contacted).");
        return Ok(());
    }
    ensure!(
        matches!(mode, "run" | "register" | "check-discord"),
        "usage: rfd-bot [run|register|import|check-config|check-storage|check-discord|healthcheck|backup|version]"
    );
    let api = Discord::new(config.token.clone(), config.app_id.clone())?;
    if mode != "register" {
        tokio::time::timeout(Duration::from_secs(20), api.check_application()).await??;
    }
    if mode == "register" {
        // Command registration also supports Cloud Run's HTTP interaction URL.
        // The Gateway-only endpoint check belongs to native run/check-discord.
        let application = tokio::time::timeout(
            Duration::from_secs(20),
            api.request(reqwest::Method::GET, "/applications/@me", None, false),
        )
        .await??;
        ensure!(
            application["id"].as_str() == Some(&config.app_id),
            "DISCORD_APP_ID does not match this bot token"
        );
        let path = if config.guild_id.is_empty() {
            format!("/applications/{}/commands", config.app_id)
        } else {
            format!(
                "/applications/{}/guilds/{}/commands",
                config.app_id, config.guild_id
            )
        };
        tokio::time::timeout(
            Duration::from_secs(20),
            api.request(
                reqwest::Method::POST,
                &path,
                Some(&commands::command()),
                false,
            ),
        )
        .await??;
        println!("RFD commands registered.");
        return Ok(());
    }
    let mut store = Store::open(&config.database)?;
    if mode == "check-discord" {
        let subs = store.subscriptions(None)?;
        let dest = subs
            .iter()
            .map(|s| (s.guild_id.clone(), s.channel_id.clone()))
            .collect::<Vec<_>>();
        tokio::time::timeout(Duration::from_secs(60), api.check_channels(&dest)).await??;
        println!(
            "Discord application and destinations for {} subscriptions are accessible (no messages sent).",
            subs.len()
        );
        return Ok(());
    }
    store.bind_application(&config.app_id)?;
    let (db, worker) = Db::new(store)?;
    let cleaner = Cleaner::new(
        db.clone(),
        config.gemini_keys.clone(),
        config.gemini_models.clone(),
    )
    .await?;
    let source = Client::new(
        Selectors::load()?,
        config.amazon_tag.clone(),
        config.bestbuy_prefix.clone(),
    )?;
    let processor = Arc::new(
        Processor::new(db.clone(), source, api.clone(), config.clone()).with_cleaner(cleaner),
    );
    let gateway = Arc::new(Gateway::new(api, commands::handler(db.clone())));
    let health = Arc::new(Health::default());
    let (stop, rx) = watch::channel(false);
    let mut tasks = JoinSet::<Result<()>>::new();
    let handler: Handler = {
        let db = db.clone();
        let gateway = gateway.clone();
        let health = health.clone();
        Arc::new(move |request| {
            let db = db.clone();
            let gateway = gateway.clone();
            let health = health.clone();
            async move {
                if request.method() != hyper::Method::GET {
                    return server::text(405, "Method not allowed");
                }
                match request.uri().path() {
                    "/health" => {
                        let ok = db
                            .call_until(Duration::from_secs(3), |s| s.ping())
                            .await
                            .is_ok();
                        server::json(if ok { 200 } else { 503 }, health.json(ok))
                    }
                    "/health/discord" => {
                        let value = gateway.health.json();
                        server::json(if value["ready"] == true { 200 } else { 503 }, value)
                    }
                    _ => server::text(404, "Not found"),
                }
            }
            .boxed()
        })
    };
    let listener = TcpListener::bind(&config.listen).await?;
    tasks.spawn(server::serve(listener, handler, rx.clone()));
    let gw = gateway.clone();
    let gw_rx = rx.clone();
    tasks.spawn(async move { gw.run(gw_rx).await });
    let job: Job = {
        let p = processor.clone();
        Arc::new(move |control: Control| {
            let p = p.clone();
            async move { p.process(control).await.map(|_| ()) }.boxed()
        })
    };
    let poll_rx = rx.clone();
    let h = health.clone();
    let interval = config.poll_interval;
    let timeout = config.poll_timeout;
    tasks.spawn(async move {
        runtime::scheduled("rfd", interval, true, timeout, poll_rx, job, Some(h)).await;
        Ok(())
    });
    let maintenance: Job = {
        let db = db.clone();
        let max = config.max_deals;
        Arc::new(move |control: Control| {
            let db = db.clone();
            async move { control.run(db.call(move |s| s.maintain(max))).await? }.boxed()
        })
    };
    let maintenance_rx = rx.clone();
    tasks.spawn(async move {
        runtime::scheduled(
            "storage maintenance",
            Duration::from_secs(86400),
            true,
            Duration::from_secs(60),
            maintenance_rx,
            maintenance,
            None,
        )
        .await;
        Ok(())
    });
    tracing::info!(listen=%config.listen,"RFD Rust runtime started");
    let result = tokio::select! {result=runtime::signal()=>result,finished=tasks.join_next()=>match finished{Some(Ok(Err(e)))=>Err(e),Some(Err(e))=>Err(e.into()),_=>Err(anyhow::anyhow!("runtime worker exited unexpectedly"))}};
    let _ = stop.send(true);
    while let Some(joined) = tasks.join_next().await {
        if let Err(e) = joined {
            tracing::warn!(error=%e,"runtime worker ended unexpectedly");
        }
    }
    drop(processor);
    drop(gateway);
    drop(db);
    tokio::task::spawn_blocking(move || worker.join())
        .await?
        .map_err(|_| anyhow::anyhow!("SQLite worker panicked"))?;
    result
}
fn import(args: &[String]) -> Result<()> {
    #[derive(clap::Parser)]
    struct Import {
        #[arg(long)]
        file: PathBuf,
        #[arg(long)]
        database: PathBuf,
        #[arg(long)]
        source_app_id: String,
        #[arg(long)]
        target_app_id: String,
    }
    use clap::Parser;
    let opts =
        Import::try_parse_from(std::iter::once("import".to_owned()).chain(args.iter().cloned()))?;
    let mut bytes = Vec::new();
    std::fs::File::open(&opts.file)?
        .take(64 * 1024 * 1024 + 1)
        .read_to_end(&mut bytes)?;
    let migration = Migration::decode(&bytes)?;
    let mut store = Store::open(&opts.database)?;
    let result = store.import(&migration, &opts.source_app_id, &opts.target_app_id)?;
    println!("{}", serde_json::to_string(&result)?);
    Ok(())
}
