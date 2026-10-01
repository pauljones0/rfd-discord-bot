//! Polls cooperate with cancellation; receipt persistence must run outside this guard.
use anyhow::{Result, ensure};
use std::{future::Future, time::Duration};
use tokio::{sync::watch, time::Instant};
#[derive(Clone)]
pub struct Control {
    pub deadline: Instant,
    stop: watch::Receiver<bool>,
}
impl Control {
    pub fn new(timeout: Duration, stop: watch::Receiver<bool>) -> Self {
        Self {
            deadline: Instant::now() + timeout,
            stop,
        }
    }
    pub fn check(&self) -> Result<()> {
        ensure!(!*self.stop.borrow(), "bot is shutting down");
        ensure!(Instant::now() < self.deadline, "poll deadline exceeded");
        Ok(())
    }
    pub async fn run<T>(&self, future: impl Future<Output = T>) -> Result<T> {
        self.check()?;
        let mut stop = self.stop.clone();
        tokio::select! {biased;result=future=>Ok(result),_=tokio::time::sleep_until(self.deadline)=>Err(anyhow::anyhow!("poll deadline exceeded")),_=stop.changed()=>Err(anyhow::anyhow!("bot is shutting down"))}
    }
    pub fn with_budget(&self, budget: Duration) -> Self {
        Self {
            deadline: self.deadline.min(Instant::now() + budget),
            stop: self.stop.clone(),
        }
    }
}
