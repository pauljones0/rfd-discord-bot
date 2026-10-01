use crate::{
    client::Client, config::Config, control::Control, db::Db, dedupe, discord::Discord,
    models::DealInfo, notifier, parse, quality, reconcile, time::Timestamp, urls,
};
use anyhow::{Context, Result, ensure};
use std::{
    collections::{BTreeMap, BTreeSet},
    time::Duration,
};
use tokio::sync::Mutex;
pub struct Processor {
    db: Db,
    source: Client,
    discord: Discord,
    config: Config,
    busy: Mutex<()>,
    cleaner: Option<crate::ai::Cleaner>,
    batch_persistence: bool,
    #[cfg(feature = "gcp")]
    free_cloud_ai: bool,
}
#[derive(Default, Debug)]
pub struct Metrics {
    pub observed: usize,
    pub reconciled: usize,
    pub sent: usize,
    pub edited: usize,
    pub created: usize,
}
impl Processor {
    pub fn new(db: Db, source: Client, discord: Discord, config: Config) -> Self {
        Self {
            db,
            source,
            discord,
            config,
            busy: Mutex::new(()),
            cleaner: None,
            batch_persistence: false,
            #[cfg(feature = "gcp")]
            free_cloud_ai: false,
        }
    }
    pub fn with_cleaner(mut self, cleaner: Option<crate::ai::Cleaner>) -> Self {
        self.cleaner = cleaner;
        self
    }
    /// Cloud checkpoints batch ordinary observations; acknowledged receipts still
    /// persist individually before the next delivery or edit.
    pub fn with_batch_persistence(mut self) -> Self {
        self.batch_persistence = true;
        self
    }
    #[cfg(feature = "gcp")]
    pub fn with_free_cloud_ai(mut self) -> Self {
        self.free_cloud_ai = true;
        self
    }
    pub async fn process(&self, control: Control) -> Result<Metrics> {
        let Ok(_guard) = self.busy.try_lock() else {
            return Ok(Metrics::default());
        };
        let mut metrics = Metrics::default();
        let cutoff =
            Timestamp::from_datetime(chrono::Utc::now() - chrono::Duration::hours(48)).nanos()?;
        let mut recent = control
            .run(self.db.call(move |s| s.recent(cutoff)))
            .await??;
        let subscriptions = control
            .run(self.db.call(|s| s.subscriptions(None)))
            .await??;
        let observations = control.run(self.source.list()).await??;
        metrics.observed = observations.len();
        let mut observations: Vec<_> = observations
            .into_iter()
            .filter_map(|mut d| match parse::assign_id(&mut d) {
                Ok(()) => Some(d),
                Err(e) => {
                    tracing::warn!(error=%e,"ignored invalid RFD card");
                    None
                }
            })
            .collect();
        let ids = observations
            .iter()
            .map(|d| d.document_id.clone())
            .collect::<Vec<_>>();
        let mut existing = control.run(self.db.call(move |s| s.by_ids(&ids))).await??;
        for d in &mut observations {
            if let Some(old) = existing.get(&d.document_id).cloned() {
                d.document_id = old.document_id.clone();
                existing.insert(old.document_id.clone(), old);
            }
        }
        let mut needed = Vec::new();
        let mut detailed = BTreeMap::new();
        for (index, d) in observations.iter_mut().enumerate() {
            let needs = existing.get(&d.document_id).is_none_or(|old| {
                let post = if old.post_url.is_empty() {
                    reconcile::primary_url(old)
                } else {
                    &old.post_url
                };
                old.actual_deal_url.is_empty()
                    || (old.description.is_empty() && old.summary.is_empty())
                    || urls::thread_key(post) != urls::thread_key(&d.post_url)
                    || old.title != d.title
            });
            if needs {
                needed.push(d.clone());
                detailed.insert(needed.len() - 1, index);
            } else {
                reconcile::preserve_details(d, &existing[&d.document_id]);
            }
        }
        let (details, stats) = control.run(self.source.details(needed)).await?;
        ensure!(
            !(stats.attempted >= 3 && stats.succeeded == 0 && stats.failed > 0),
            "RFD detail fetch unhealthy: attempted={} failed={} not_found={}",
            stats.attempted,
            stats.failed,
            stats.not_found
        );
        for (index, d) in details.into_iter().enumerate() {
            observations[detailed[&index]] = d;
        }
        let observations = dedupe::deduplicate(observations, &mut existing, &mut recent);
        let observations = dedupe::by_detailed_url(observations, &mut existing, &recent);
        let mut groups = BTreeMap::<String, Vec<DealInfo>>::new();
        for d in observations {
            groups.entry(d.document_id.clone()).or_default().push(d);
        }
        let mut deals: Vec<_> = groups
            .into_iter()
            .filter_map(|(id, g)| reconcile::reconcile(existing.get(&id), &g))
            .collect();
        deals.sort_by(|a, b| {
            a.published_timestamp
                .parse()
                .ok()
                .cmp(&b.published_timestamp.parse().ok())
                .then(a.document_id.cmp(&b.document_id))
        });
        metrics.reconciled = deals.len();
        #[cfg(feature = "gcp")]
        if self.free_cloud_ai
            && deals
                .iter()
                .any(|d| !d.ai_processed || d.clean_title.is_empty())
        {
            // Lease/checkpoint restoration already completed. Verify only when
            // titles need cleanup, immediately before calling the provider.
            if let Some(cleaner) = crate::gcp_ai::cleaner(&self.db, &self.config, &control).await {
                cleaner.clean(&mut deals, &control).await;
            }
        }
        if let Some(cleaner) = &self.cleaner {
            cleaner.clean(&mut deals, &control).await;
        }
        let mut failures = Vec::new();
        let mut creates = Vec::new();
        let mut updates = Vec::new();
        for mut deal in deals {
            control.check()?;
            let previous = existing.get(&deal.document_id);
            let mut stored = previous.is_some();
            quality::apply(&mut deal);
            if previous != Some(&deal) {
                deal.last_updated = Timestamp::now();
            }
            let previous_receipts = deal.discord_message_ids.clone();
            let previous_owners = deal.discord_message_application_ids.clone();
            let mut seen = BTreeSet::new();
            if !deal.post_url.is_empty() || !deal.threads.is_empty() {
                for sub in &subscriptions {
                    if !seen.insert(sub.channel_id.clone())
                        || deal
                            .discord_message_ids
                            .get(&sub.channel_id)
                            .is_some_and(|v| !v.is_empty())
                        || !quality::eligible(&deal, sub)
                    {
                        continue;
                    }
                    let payload = notifier::delivery(&deal, &self.config.app_id, &sub.channel_id);
                    match control
                        .run(self.discord.send(&sub.channel_id, &payload))
                        .await
                    {
                        Ok(Ok(id)) => {
                            deal.discord_message_ids.insert(sub.channel_id.clone(), id);
                            deal.discord_message_application_ids
                                .insert(sub.channel_id.clone(), self.config.app_id.clone());
                            if previous_receipts.is_empty() {
                                deal.discord_last_updated_time = Timestamp::now();
                            }
                            // Persist each acknowledged channel immediately, even if shutdown arrived during POST.
                            self.save(&deal, stored)
                                .await
                                .context("save acknowledged Discord receipt")?;
                            if !stored {
                                stored = true;
                                metrics.created += 1;
                            }
                            metrics.sent += 1;
                        }
                        Ok(Err(e)) => failures.push(format!("channel {}: {e}", sub.channel_id)),
                        Err(e) => {
                            failures.push(e.to_string());
                            break;
                        }
                    }
                }
            }
            let now = chrono::Utc::now();
            let checkpoint = deal.discord_last_updated_time.parse()?;
            if !previous_receipts.is_empty()
                && deal.last_updated.parse()? > checkpoint
                && now
                    .signed_duration_since(checkpoint)
                    .to_std()
                    .unwrap_or_default()
                    >= self.config.update_interval
                && now.signed_duration_since(deal.published_timestamp.parse()?)
                    < chrono::Duration::hours(2)
            {
                let payload = notifier::payload(&deal);
                let mut complete = true;
                for (channel, id) in &previous_receipts {
                    if previous_owners
                        .get(channel)
                        .is_some_and(|owner| !owner.is_empty() && owner != &self.config.app_id)
                    {
                        continue;
                    }
                    match control.run(self.discord.edit(channel, id, &payload)).await {
                        Ok(Ok(())) => metrics.edited += 1,
                        Ok(Err(e)) => {
                            complete = false;
                            failures.push(format!("edit channel {channel}: {e}"));
                        }
                        Err(e) => {
                            complete = false;
                            failures.push(e.to_string());
                            break;
                        }
                    }
                }
                if complete {
                    deal.discord_last_updated_time = Timestamp::now();
                }
            }
            if previous != Some(&deal) {
                if self.batch_persistence {
                    if stored {
                        updates.push(deal);
                    } else {
                        creates.push(deal);
                    }
                } else {
                    self.save(&deal, stored).await?;
                }
                if !stored {
                    metrics.created += 1;
                }
            }
        }
        if !creates.is_empty() || !updates.is_empty() {
            self.db
                .call(move |s| s.batch_write(&creates, &updates))
                .await?;
        }
        if metrics.created > 0 {
            let max = self.config.max_deals;
            self.db.call(move |s| s.maintain(max)).await?;
        }
        control.check()?;
        tracing::info!(
            observed = metrics.observed,
            reconciled = metrics.reconciled,
            sent = metrics.sent,
            edited = metrics.edited,
            created = metrics.created,
            "RFD poll complete"
        );
        ensure!(
            failures.is_empty(),
            "{} RFD deliveries or edits failed: {}",
            failures.len(),
            failures.join("; ")
        );
        Ok(metrics)
    }
    async fn save(&self, deal: &DealInfo, exists: bool) -> Result<()> {
        let deal = deal.clone();
        self.db
            .call_until(Duration::from_secs(5), move |s| {
                if exists {
                    s.batch_write(&[], &[deal])
                } else {
                    s.batch_write(&[deal], &[])
                }
            })
            .await
    }
}
