use crate::control::Control;
use anyhow::Result;
use futures_util::future::BoxFuture;
use std::{
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicI64, Ordering},
    },
    time::Duration,
};
use tokio::sync::watch;
#[derive(Default)]
pub struct Health {
    attempt: AtomicI64,
    success: AtomicI64,
    failed: AtomicBool,
}
impl Health {
    pub fn json(&self, ok: bool) -> serde_json::Value {
        serde_json::json!({"ok":ok,"last_poll_attempt":self.attempt.load(Ordering::Relaxed),"last_successful_poll":self.success.load(Ordering::Relaxed),"last_poll_failed":self.failed.load(Ordering::Relaxed)})
    }
}
pub type Job = Arc<dyn Fn(Control) -> BoxFuture<'static, Result<()>> + Send + Sync>;
pub async fn scheduled(
    name: &'static str,
    interval: Duration,
    immediate: bool,
    timeout: Duration,
    mut stop: watch::Receiver<bool>,
    job: Job,
    health: Option<Arc<Health>>,
) {
    let mut delay = if immediate { Duration::ZERO } else { interval };
    loop {
        if *stop.borrow() {
            break;
        }
        tokio::select! {_=tokio::time::sleep(delay)=>{},_=stop.changed()=>break,}
        if let Some(h) = &health {
            h.attempt
                .store(chrono::Utc::now().timestamp(), Ordering::Relaxed);
        }
        let start = tokio::time::Instant::now();
        // The job owns cancellation. Dropping a whole poll could discard an acknowledged receipt.
        let result = job(Control::new(timeout, stop.clone())).await;
        if let Some(h) = &health {
            h.failed.store(result.is_err(), Ordering::Relaxed);
            if result.is_ok() {
                h.success
                    .store(chrono::Utc::now().timestamp(), Ordering::Relaxed);
            }
        }
        if let Err(error) = result
            && !*stop.borrow()
        {
            tracing::error!(job=name,error=%error,elapsed_ms=start.elapsed().as_millis(),"scheduled job failed");
        }
        delay = interval;
    }
}
pub async fn signal() -> Result<()> {
    let mut terminate = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())?;
    tokio::select! {result=tokio::signal::ctrl_c()=>result?,_=terminate.recv()=>{}}
    Ok(())
}
pub fn start_logging() {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| crate::env::value("LOG_LEVEL", "info").to_lowercase().into()),
        )
        .with_target(false)
        .init();
}
pub fn executor() -> Result<tokio::runtime::Runtime> {
    Ok(tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .max_blocking_threads(4)
        .thread_stack_size(512 * 1024)
        .build()?)
}
