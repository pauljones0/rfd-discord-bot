use crate::models::{DealInfo, GeminiQuotaStatus, Subscription};
use anyhow::{Result, bail, ensure};
use rusqlite::{Connection, OptionalExtension, params};
use std::{
    collections::{BTreeMap, BTreeSet},
    path::Path,
};

pub const APPLICATION_KEY: &str = "discord-application-id";
pub struct Store {
    pub(crate) db: Connection,
    #[cfg(feature = "gcp")]
    pub(crate) cloud: Option<crate::gcp_storage::CloudState>,
    #[cfg(feature = "gcp")]
    pub(crate) cloud_subscriptions: Option<crate::gcp_storage::CloudState>,
}
impl Store {
    pub fn open(path: &Path) -> Result<Self> {
        if let Some(parent) = path.parent().filter(|p| !p.as_os_str().is_empty()) {
            std::fs::create_dir_all(parent)?;
        }
        let mut db = Connection::open(path)?;
        validate_schema(&db)?;
        validate_legacy_text(&db)?; // Reads only, before changing a foreign database.
        let existed: bool = db.query_row(
            "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='deal_threads')",
            [],
            |r| r.get(0),
        )?;
        // WAL must be changed outside a transaction; schema validation has completed.
        db.execute_batch("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA cache_size=-2048; PRAGMA wal_autocheckpoint=256; PRAGMA journal_size_limit=1048576; PRAGMA busy_timeout=5000;")?;
        let tx = db.transaction()?;
        tx.execute_batch(include_str!("schema.sql"))?;
        normalize_legacy_text(&tx)?;
        if !existed {
            tx.execute_batch(include_str!("thread-backfill.sql"))?;
        }
        tx.commit()?;
        Ok(Self {
            db,
            #[cfg(feature = "gcp")]
            cloud: None,
            #[cfg(feature = "gcp")]
            cloud_subscriptions: None,
        })
    }
    pub fn ping(&self) -> Result<()> {
        self.db.query_row("SELECT 1", [], |_| Ok(()))?;
        Ok(())
    }
    pub fn integrity_check(&self) -> Result<()> {
        let v: String = self
            .db
            .query_row("PRAGMA integrity_check", [], |r| r.get(0))?;
        ensure!(v == "ok", "SQLite integrity check: {v}");
        Ok(())
    }
    pub fn bind_application(&mut self, app: &str) -> Result<()> {
        ensure!(valid_discord_id(app), "invalid Discord application ID");
        let tx = self.db.transaction()?;
        let existing: Option<String> = tx
            .query_row(
                "SELECT payload FROM settings WHERE key=?",
                [APPLICATION_KEY],
                |r| r.get(0),
            )
            .optional()?;
        if let Some(existing) = existing {
            ensure!(
                existing == app,
                "database belongs to a different Discord application"
            );
        } else {
            let count: i64 = tx.query_row(
                "SELECT (SELECT COUNT(*) FROM deals)+(SELECT COUNT(*) FROM subscriptions)",
                [],
                |r| r.get(0),
            )?;
            ensure!(
                count == 0,
                "nonempty database has no application binding; use an explicit migration"
            );
            tx.execute(
                "INSERT INTO settings(key,payload) VALUES(?,?)",
                params![APPLICATION_KEY, app],
            )?;
        }
        tx.commit()?;
        Ok(())
    }
    pub fn deal(&self, id: &str) -> Result<Option<DealInfo>> {
        let raw: Option<String> = self
            .db
            .query_row("SELECT payload FROM deals WHERE id=?", [id], |r| r.get(0))
            .optional()?;
        raw.map(|raw| decode_deal(id, &raw)).transpose()
    }
    fn query_deals(&self, sql: &str, args: impl rusqlite::Params) -> Result<Vec<DealInfo>> {
        let mut stmt = self.db.prepare_cached(sql)?;
        let rows = stmt.query_map(args, |r| {
            Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?))
        })?;
        rows.map(|r| {
            let (id, raw) = r?;
            decode_deal(&id, &raw)
        })
        .collect()
    }
    pub fn recent(&self, since_nanos: i64) -> Result<Vec<DealInfo>> {
        self.query_deals(
            "SELECT id,payload FROM deals WHERE published_at>=? ORDER BY published_at,id",
            [since_nanos],
        )
    }
    pub fn by_ids(&self, ids: &[String]) -> Result<BTreeMap<String, DealInfo>> {
        let mut out = BTreeMap::new();
        for batch in ids.chunks(900) {
            let marks = vec!["?"; batch.len()].join(",");
            for d in self.query_deals(
                &format!("SELECT id,payload FROM deals WHERE id IN ({marks})"),
                rusqlite::params_from_iter(batch),
            )? {
                out.insert(d.document_id.clone(), d);
            }
        }
        let missing: Vec<_> = ids
            .iter()
            .filter(|i| !out.contains_key(*i))
            .cloned()
            .collect();
        for batch in missing.chunks(900) {
            let wanted: BTreeSet<_> = batch.iter().collect();
            let marks = vec!["?"; batch.len()].join(",");
            for d in self.query_deals(&format!("SELECT id,payload FROM deals WHERE id IN (SELECT deal_id FROM deal_threads WHERE thread_id IN ({marks}))"),rusqlite::params_from_iter(batch))? {
                for t in &d.threads {
                    if !wanted.contains(&t.document_id) {continue;}
                    if let Some(old)=out.get(&t.document_id) { ensure!(old.document_id==d.document_id,"thread {} has ambiguous stored ownership",t.document_id); }
                    out.insert(t.document_id.clone(),d.clone());
                }
            }
        }
        Ok(out)
    }
    pub fn batch_write(&mut self, creates: &[DealInfo], updates: &[DealInfo]) -> Result<()> {
        let tx = self.db.transaction()?;
        for (items, create) in [(creates, true), (updates, false)] {
            let mut sql =
                "INSERT INTO deals(id,payload,published_at,updated_at) VALUES(?,?,?,?)".to_string();
            if !create {
                sql.push_str(" ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,published_at=excluded.published_at,updated_at=excluded.updated_at");
            }
            let mut stmt = tx.prepare_cached(&sql)?;
            for d in items {
                ensure!(!d.document_id.is_empty(), "missing deal identity");
                stmt.execute(params![
                    d.document_id,
                    serde_json::to_string(d)?,
                    d.published_timestamp.nanos()?,
                    d.last_updated.nanos()?
                ])?;
            }
        }
        tx.commit()?;
        Ok(())
    }
    pub fn maintain(&self, max: usize) -> Result<()> {
        ensure!(max > 0, "retention limit must be positive");
        self.db.execute("DELETE FROM deals WHERE id IN (SELECT id FROM deals ORDER BY updated_at DESC,id LIMIT -1 OFFSET ?)",[max as i64])?;
        self.db
            .execute_batch("PRAGMA wal_checkpoint(PASSIVE); PRAGMA optimize;")?;
        Ok(())
    }
    pub fn subscriptions(&self, guild: Option<&str>) -> Result<Vec<Subscription>> {
        let mut stmt=self.db.prepare_cached("SELECT guild_id,channel_id,filter,payload FROM subscriptions WHERE (? IS NULL OR guild_id=?) ORDER BY guild_id,channel_id,filter")?;
        let mut rows = stmt.query(params![guild, guild])?;
        let mut out = Vec::new();
        while let Some(r) = rows.next()? {
            let sub: Subscription = serde_json::from_str(&r.get::<_, String>(3)?)?;
            ensure!(
                sub.guild_id == r.get::<_, String>(0)?
                    && sub.channel_id == r.get::<_, String>(1)?
                    && sub.deal_type == r.get::<_, String>(2)?
                    && valid_subscription(&sub),
                "subscription has inconsistent stored scope"
            );
            out.push(sub);
        }
        Ok(out)
    }
    pub fn save_subscription(&self, sub: &Subscription) -> Result<()> {
        ensure!(valid_subscription(sub), "invalid RFD subscription");
        let mut sub = sub.clone();
        sub.subscription_type = "rfd".into();
        self.db.execute("INSERT INTO subscriptions(guild_id,channel_id,filter,payload) VALUES(?,?,?,?) ON CONFLICT(guild_id,channel_id,filter) DO UPDATE SET payload=excluded.payload",params![sub.guild_id,sub.channel_id,sub.deal_type,serde_json::to_string(&sub)?])?;
        Ok(())
    }
    pub fn remove_subscription(
        &self,
        guild: &str,
        channel: &str,
        filter: Option<&str>,
    ) -> Result<()> {
        ensure!(
            !guild.is_empty() && !channel.is_empty(),
            "guild and channel are required"
        );
        self.db.execute("DELETE FROM subscriptions WHERE guild_id=? AND channel_id=? AND (? IS NULL OR filter=?)",params![guild,channel,filter,filter])?;
        Ok(())
    }
    pub fn quota(&self) -> Result<Option<GeminiQuotaStatus>> {
        let raw: Option<String> = self
            .db
            .query_row(
                "SELECT payload FROM settings WHERE key='gemini-quota'",
                [],
                |r| r.get(0),
            )
            .optional()?;
        raw.map(|s| Ok(serde_json::from_str(&s)?)).transpose()
    }
    pub fn save_quota(&self, q: &GeminiQuotaStatus) -> Result<()> {
        self.db.execute("INSERT INTO settings(key,payload) VALUES('gemini-quota',?) ON CONFLICT(key) DO UPDATE SET payload=excluded.payload",[serde_json::to_string(q)?])?;
        Ok(())
    }
}
pub fn valid_filter(v: &str) -> bool {
    matches!(
        v,
        "rfd_all" | "rfd_tech" | "rfd_warm_hot" | "rfd_warm_hot_tech" | "rfd_hot" | "rfd_hot_tech"
    )
}
pub fn valid_subscription(s: &Subscription) -> bool {
    !s.guild_id.is_empty()
        && !s.channel_id.is_empty()
        && valid_filter(&s.deal_type)
        && (s.subscription_type.is_empty() || s.subscription_type == "rfd")
}
pub fn valid_discord_id(v: &str) -> bool {
    v.parse::<u64>().is_ok_and(|n| n > 0 && n.to_string() == v)
}
fn decode_deal(id: &str, raw: &str) -> Result<DealInfo> {
    let d: DealInfo = serde_json::from_str(raw)?;
    ensure!(
        !id.is_empty() && d.document_id == id,
        "deal {id:?} has inconsistent stored identity"
    );
    Ok(d)
}
pub(crate) fn validate_schema(db: &Connection) -> Result<()> {
    let mut stmt=db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name NOT GLOB 'sqlite_*' ORDER BY name")?;
    let names = stmt
        .query_map([], |r| r.get::<_, String>(0))?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    for name in names {
        let expected: Vec<(&str, &str, i64, i64)> = match name.as_str() {
            "deals" => vec![
                ("id", "TEXT", 0, 1),
                ("payload", "TEXT", 1, 0),
                ("published_at", "INTEGER", 1, 0),
                ("updated_at", "INTEGER", 1, 0),
            ],
            "subscriptions" => vec![
                ("guild_id", "TEXT", 1, 1),
                ("channel_id", "TEXT", 1, 2),
                ("filter", "TEXT", 1, 3),
                ("payload", "TEXT", 1, 0),
            ],
            "settings" => vec![("key", "TEXT", 0, 1), ("payload", "TEXT", 1, 0)],
            "deal_threads" => vec![("thread_id", "TEXT", 1, 1), ("deal_id", "TEXT", 1, 2)],
            _ => bail!("destination is not a standalone RFD database: foreign table {name:?}"),
        };
        let mut stmt = db.prepare(&format!("PRAGMA table_xinfo(\"{name}\")"))?;
        let columns = stmt
            .query_map([], |r| {
                Ok((
                    r.get::<_, String>(1)?,
                    r.get::<_, String>(2)?,
                    r.get::<_, i64>(3)?,
                    r.get::<_, Option<String>>(4)?,
                    r.get::<_, i64>(5)?,
                    r.get::<_, i64>(6)?,
                ))
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        ensure!(
            columns.len() == expected.len(),
            "incompatible {name} schema"
        );
        for ((n, t, nn, d, pk, h), (en, et, enn, epk)) in columns.iter().zip(expected) {
            ensure!(
                n == en
                    && t.trim().eq_ignore_ascii_case(et)
                    && *nn == enn
                    && *pk == epk
                    && d.is_none()
                    && *h == 0,
                "incompatible {name} schema"
            );
        }
    }
    Ok(())
}

// Go's database/sql binds json.Marshal's []byte output as BLOB even when
// a column has TEXT affinity. Preserve the exact UTF-8 JSON bytes on upgrade.
const LEGACY_TEXT: &[(&str, &str)] = &[
    ("deals", "payload"),
    ("subscriptions", "payload"),
    ("settings", "payload"),
];
fn validate_legacy_text(db: &Connection) -> Result<()> {
    for &(table, column) in LEGACY_TEXT {
        let exists: bool = db.query_row(
            "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)",
            [table],
            |r| r.get(0),
        )?;
        if !exists {
            continue;
        }
        let mut stmt = db.prepare(&format!(
            "SELECT {column} FROM {table} WHERE typeof({column})='blob'"
        ))?;
        let rows = stmt.query_map([], |r| r.get::<_, Vec<u8>>(0))?;
        for row in rows {
            std::str::from_utf8(&row?)
                .map_err(|_| anyhow::anyhow!("legacy JSON BLOB is not valid UTF-8"))?;
        }
    }
    Ok(())
}
fn normalize_legacy_text(db: &Connection) -> Result<()> {
    for &(table, column) in LEGACY_TEXT {
        db.execute(
            &format!(
                "UPDATE {table} SET {column}=CAST({column} AS TEXT) WHERE typeof({column})='blob'"
            ),
            [],
        )?;
    }
    Ok(())
}
