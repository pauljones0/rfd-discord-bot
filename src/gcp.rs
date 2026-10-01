//! Request-driven Cloud Run adapter. No Gateway or local scheduler is started.
use crate::{
    commands,
    control::Control,
    db::Db,
    env,
    gcp_storage::CloudState,
    server::{self, Handler, Reply},
    storage::Store,
};
use anyhow::{Context, Result, ensure};
use futures_util::FutureExt;
use hyper::{Request, body::Incoming};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{sync::Arc, time::Duration};
use tokio::{
    net::TcpListener,
    sync::{Mutex, watch},
};

struct App {
    db: Db,
    work: Option<Work>,
    key: ed25519_dalek::VerifyingKey,
    admin: String,
    gate: Mutex<()>,
    stop: watch::Receiver<bool>,
    fixture: bool,
    commands_only: bool,
    app_id: String,
}
pub async fn run() -> Result<()> {
    let project = env::value("GCP_PROJECT", "");
    let namespace = env::value("FIRESTORE_NAMESPACE", "");
    let mode = env::value("BOT_DEPLOYMENT_MODE", "fixture");
    ensure!(
        matches!(mode.as_str(), "fixture" | "production"),
        "invalid deployment mode"
    );
    let fixture = mode == "fixture";
    let role = env::value("BOT_REQUEST_ROLE", "worker");
    ensure!(
        matches!(role.as_str(), "worker" | "commands"),
        "invalid request role"
    );
    let commands_only = role == "commands";
    let subscriptions_namespace = env::value("SUBSCRIPTIONS_NAMESPACE", "");
    ensure!(
        commands_only
            || (subscriptions_namespace.ends_with(&format!("-{mode}"))
                && subscriptions_namespace != namespace),
        "worker requires a distinct subscription namespace"
    );
    ensure!(
        !fixture || namespace.ends_with("-fixture"),
        "fixtures require a separate -fixture namespace"
    );
    ensure!(
        fixture || namespace.ends_with("-production"),
        "production requires a separate -production namespace"
    );
    let app_id = env::value("DISCORD_APP_ID", "1001");
    ensure!(
        crate::discord::valid_id(&app_id),
        "invalid Discord application ID"
    );
    let admin = env::value("SCHEDULER_TOKEN", "");
    ensure!(
        admin.len() >= 32,
        "SCHEDULER_TOKEN must be at least 32 characters"
    );
    let key: [u8; 32] = hex::decode(env::value("DISCORD_PUBLIC_KEY", ""))?
        .try_into()
        .map_err(|_| anyhow::anyhow!("DISCORD_PUBLIC_KEY must encode 32 bytes"))?;
    let key = ed25519_dalek::VerifyingKey::from_bytes(&key)?;
    let cloud_project = project.clone();
    let cloud_namespace = namespace.clone();
    let cloud_mode = mode.clone();
    let cloud_app_id = app_id.clone();
    let store = tokio::task::spawn_blocking(move || -> Result<Store> {
        let mut store = Store::open(std::path::Path::new(":memory:"))?;
        let state = CloudState::new(
            &cloud_project,
            &cloud_namespace,
            &format!("rfd:{cloud_mode}:{cloud_app_id}"),
        )?;
        store.cloud = Some(if commands_only {
            state.commands_only()
        } else {
            state
        });
        if !commands_only {
            store.cloud_subscriptions = Some(CloudState::new(
                &cloud_project,
                &subscriptions_namespace,
                &format!("rfd:{cloud_mode}:{cloud_app_id}"),
            )?);
        }
        Ok(store)
    })
    .await??;
    let (db, worker) = Db::new(store)?;
    let work = if fixture || commands_only {
        None
    } else {
        Some(Work::new(db.clone())?)
    };
    let (stop, rx) = watch::channel(false);
    let app = Arc::new(App {
        db: db.clone(),
        work,
        key,
        admin,
        gate: Mutex::new(()),
        stop: rx.clone(),
        fixture,
        commands_only,
        app_id,
    });
    let handler: Handler = {
        let app = app.clone();
        Arc::new(move |request| {
            let app = app.clone();
            async move { app.route(request).await }.boxed()
        })
    };
    let listen = format!("0.0.0.0:{}", env::value("PORT", "8080"));
    let listener = TcpListener::bind(&listen).await?;
    tracing::info!(%mode,%namespace,%listen,"Cloud Run request runtime ready");
    let server = tokio::spawn(server::serve(listener, handler, rx));
    crate::runtime::signal().await?;
    let _ = stop.send(true);
    server.await??;
    drop(app);
    drop(db);
    tokio::task::spawn_blocking(move || worker.join())
        .await?
        .map_err(|_| anyhow::anyhow!("cloud SQLite worker panicked"))?;
    Ok(())
}
impl App {
    async fn route(&self, request: Request<Incoming>) -> Reply {
        let path = request.uri().path().to_owned();
        if request.method() == hyper::Method::GET && matches!(path.as_str(), "/" | "/health") {
            return server::json(
                200,
                json!({"ready":true,"storage":"firestore-checkpoint","mode":if self.fixture {"fixture"}else{"production"},"role":if self.commands_only {"commands"}else{"worker"}}),
            );
        }
        if request.method() != hyper::Method::POST {
            return server::text(405, "Method not allowed");
        }
        let interaction = path == "/discord/interactions";
        if !interaction && path != "/tick" {
            return server::text(404, "Not found");
        }
        let timestamp = request
            .headers()
            .get("x-signature-timestamp")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
            .to_owned();
        let signature = request
            .headers()
            .get("x-signature-ed25519")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
            .to_owned();
        if !interaction {
            let supplied = request
                .headers()
                .get("x-bot-scheduler-token")
                .and_then(|v| v.to_str().ok())
                .unwrap_or("");
            if !constant_equal(supplied.as_bytes(), self.admin.as_bytes()) {
                return server::text(401, "Unauthorized");
            }
        }
        let body = match tokio::time::timeout(
            Duration::from_secs(2),
            server::body(request.into_body(), 16 * 1024),
        )
        .await
        {
            Ok(Ok(body)) => body,
            _ => return server::text(413, "Invalid request body"),
        };
        if interaction && !verify(&self.key, &signature, &timestamp, &body) {
            return server::text(401, "Invalid request signature");
        }
        let payload: Value = match serde_json::from_slice(&body) {
            Ok(v) => v,
            Err(_) => return server::text(400, "Invalid JSON"),
        };
        // PINGs never need state, preserving cold-start validation speed.
        if interaction && payload["type"] == 1 {
            return server::json(200, json!({"type":1}));
        }
        if interaction != self.commands_only {
            return server::text(404, "Request belongs to the other service role");
        }
        let Ok(_guard) = self.gate.try_lock() else {
            return server::text(429, "A bot request is already running");
        };
        let start = std::time::Instant::now();
        let result = self.session(interaction, payload).await;
        tracing::info!(
            interaction,
            fixture = self.fixture,
            elapsed_ms = start.elapsed().as_millis(),
            ok = result.is_ok(),
            "Cloud Run request completed"
        );
        match result {
            Ok(v) => server::json(200, v),
            Err(e) => {
                tracing::warn!(error=%e,"Cloud Run request failed");
                server::text(503, "Bot request failed; state was not acknowledged")
            }
        }
    }
    async fn session(&self, interaction: bool, payload: Value) -> Result<Value> {
        if let Err(error) = self.db.cloud_session(true).await {
            // A failed restore can still own the lease. Release only our fenced
            // manifest so corruption does not block the next instance for 35m.
            let _ = self.db.cloud_session(false).await;
            return Err(error);
        }
        let id = self.app_id.clone();
        let binding = self.db.call(move |s| s.bind_application(&id)).await;
        let result = if let Err(error) = binding {
            Err(error)
        } else if interaction {
            let response = command(&self.db, &payload).await;
            Ok(response)
        } else if let Err(error) = self.db.call(|s| s.cloud_sync_subscriptions()).await {
            Err(error)
        } else if let Some(work) = &self.work {
            work.tick(Control::new(
                Duration::from_secs(28 * 60),
                self.stop.clone(),
            ))
            .await
        } else {
            fixture_tick(&self.db).await
        };
        // Release synchronously while CPU is allocated to this HTTP request.
        let release = self.db.cloud_session(false).await;
        release?;
        result
    }
}
fn constant_equal(a: &[u8], b: &[u8]) -> bool {
    Sha256::digest(a)
        .iter()
        .zip(Sha256::digest(b).iter())
        .fold(0u8, |n, (a, b)| n | (a ^ b))
        == 0
}
fn verify(
    key: &ed25519_dalek::VerifyingKey,
    signature: &str,
    timestamp: &str,
    body: &[u8],
) -> bool {
    let Some(seconds) = timestamp.parse::<i64>().ok() else {
        return false;
    };
    if chrono::Utc::now().timestamp().abs_diff(seconds) > 300 {
        return false;
    }
    let Ok(bytes) = hex::decode(signature) else {
        return false;
    };
    let Ok(signature) = ed25519_dalek::Signature::from_slice(&bytes) else {
        return false;
    };
    let mut message = timestamp.as_bytes().to_vec();
    message.extend_from_slice(body);
    key.verify_strict(&message, &signature).is_ok()
}

async fn command(db: &Db, payload: &Value) -> Value {
    crate::discord::private_reply(commands::handle(db, payload).await)
}
struct Work {
    processor: crate::processor::Processor,
    db: Db,
    config: crate::config::Config,
}
impl Work {
    fn new(db: Db) -> Result<Self> {
        let config = crate::config::Config::load()?;
        let api = crate::discord::Discord::new(config.token.clone(), config.app_id.clone())?;
        let source = crate::client::Client::new(
            crate::parse::Selectors::load()?,
            config.amazon_tag.clone(),
            config.bestbuy_prefix.clone(),
        )?;
        let processor = crate::processor::Processor::new(db.clone(), source, api, config.clone())
            .with_batch_persistence()
            .with_free_cloud_ai();
        Ok(Self {
            processor,
            db,
            config,
        })
    }
    async fn tick(&self, control: Control) -> Result<Value> {
        let id = self.config.app_id.clone();
        let max = self.config.max_deals;
        self.db
            .call(move |s| {
                s.bind_application(&id)?;
                s.maintain(max)
            })
            .await?;
        let m = self
            .processor
            .process(control.with_budget(self.config.poll_timeout))
            .await?;
        Ok(json!({"observed":m.observed,"sent":m.sent,"edited":m.edited}))
    }
}
async fn fixture_tick(db: &Db) -> Result<Value> {
    use crate::{models::DealInfo, time::Timestamp};
    db.call(|s| {
        s.bind_application("1001")?;
        let counter=s.recent(i64::MIN)?.iter().map(|d|d.document_id.trim_start_matches("fixture-").parse::<u64>().unwrap_or(0)).max().unwrap_or(0)+1;
        let d=DealInfo {document_id:format!("fixture-{counter}"),title:"Cloud fixture only".into(),published_timestamp:Timestamp::now(),last_updated:Timestamp::now(),discord_message_ids:std::collections::BTreeMap::from([("42".into(),format!("receipt-{counter}"))]),discord_message_application_ids:std::collections::BTreeMap::from([("42".into(),"1001".into())]),..Default::default()};
        s.batch_write(&[d],&[])?; s.maintain(2)?;
        ensure!(s.deal(&format!("fixture-{counter}"))?.context("fixture receipt missing")?.discord_message_ids["42"]==format!("receipt-{counter}"),"fixture receipt changed");
        Ok(json!({"fixture":true,"counter":counter,"retained":s.recent(i64::MIN)?.len(),"subscriptions":s.subscriptions(None)?.len(),"integrity":s.integrity_check().is_ok()}))
    }).await
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn interaction_signatures_bind_body_and_fresh_timestamp() {
        let key = ed25519_dalek::SigningKey::from_bytes(&[7u8; 32]);
        use ed25519_dalek::Signer;
        let timestamp = chrono::Utc::now().timestamp().to_string();
        let body = br#"{"type":1}"#;
        let mut message = timestamp.as_bytes().to_vec();
        message.extend_from_slice(body);
        let signature = hex::encode(key.sign(&message).to_bytes());
        assert!(verify(&key.verifying_key(), &signature, &timestamp, body));
        assert!(!verify(
            &key.verifying_key(),
            &signature,
            &timestamp,
            b"forged"
        ));
        assert!(!verify(&key.verifying_key(), &signature, "0", body));
        assert!(!verify(&key.verifying_key(), "invalid", &timestamp, body));
    }
}
