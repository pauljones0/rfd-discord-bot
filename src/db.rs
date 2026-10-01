//! One dedicated SQLite worker keeps disk waits off the Gateway/HTTP executor.
use crate::storage::Store;
use anyhow::{Result, ensure};
use std::{
    thread::JoinHandle,
    time::{Duration, Instant},
};
use tokio::sync::{mpsc, oneshot};
type Job = Box<dyn FnOnce(&mut Store) + Send>;
#[derive(Clone)]
pub struct Db {
    sender: mpsc::Sender<Job>,
}
impl Db {
    pub fn new(mut store: Store) -> Result<(Self, JoinHandle<()>)> {
        let (sender, mut receiver) = mpsc::channel::<Job>(16);
        let thread = std::thread::Builder::new()
            .name("bot-sqlite".into())
            .stack_size(512 * 1024)
            .spawn(move || {
                while let Some(job) = receiver.blocking_recv() {
                    job(&mut store);
                }
            })?;
        Ok((Self { sender }, thread))
    }
    pub async fn call<T, F>(&self, f: F) -> Result<T>
    where
        T: Send + 'static,
        F: FnOnce(&mut Store) -> Result<T> + Send + 'static,
    {
        self.call_until(Duration::from_secs(10), f).await
    }
    pub async fn call_until<T, F>(&self, timeout: Duration, f: F) -> Result<T>
    where
        T: Send + 'static,
        F: FnOnce(&mut Store) -> Result<T> + Send + 'static,
    {
        let (tx, rx) = oneshot::channel();
        let deadline = Instant::now() + timeout;
        let job: Job = Box::new(move |store| {
            if tx.is_closed() || Instant::now() >= deadline {
                return;
            }
            let remaining = deadline.saturating_duration_since(Instant::now());
            let result = (|| {
                store
                    .db
                    .busy_timeout(remaining.min(Duration::from_secs(5)))?;
                #[cfg(feature = "gcp")]
                store.cloud_ready()?;
                let result = f(store);
                #[cfg(feature = "gcp")]
                store.cloud_checkpoint()?;
                store.db.busy_timeout(Duration::from_secs(5))?;
                result
            })();
            let _ = tx.send(result);
        });

        tokio::time::timeout(timeout, async {
            ensure!(self.sender.send(job).await.is_ok(), "SQLite worker stopped");
            rx.await
                .map_err(|_| anyhow::anyhow!("SQLite operation expired or worker stopped"))?
        })
        .await
        .map_err(|_| anyhow::anyhow!("SQLite operation timed out"))?
    }
    #[cfg(feature = "gcp")]
    pub async fn cloud_session(&self, begin: bool) -> Result<()> {
        let (tx, rx) = oneshot::channel();
        let job: Job = Box::new(move |store| {
            if tx.is_closed() {
                return;
            }
            let result = if begin {
                store.cloud_begin()
            } else {
                store.cloud_end()
            };
            if tx.send(result).is_err() && begin {
                let _ = store.cloud_end();
            }
        });
        ensure!(self.sender.send(job).await.is_ok(), "SQLite worker stopped");
        rx.await
            .map_err(|_| anyhow::anyhow!("cloud worker stopped"))?
    }
}
