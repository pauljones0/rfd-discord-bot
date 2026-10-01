//! Firestore is authoritative; SQLite is a disposable, leased working copy.
//! Checkpoints and lease fencing commit atomically. No background persistence.
use anyhow::{Context, Result, bail, ensure};
use base64::{Engine, engine::general_purpose::STANDARD};
use flate2::{Compression, read::GzDecoder, write::GzEncoder};
use reqwest::{Method, blocking::Client};
use rusqlite::{Connection, types::Value as SqlValue};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeMap,
    io::{Read, Write},
    time::{Duration, Instant},
};

const CHUNK: usize = 512 * 1024;
const MAX_CHUNKS: usize = 12;
const MAX_RAW: usize = 32 * 1024 * 1024;
const REQUEST_RESERVE: u64 = 1810;
const RUNTIME_LIMIT: u64 = 650_000;
const LEASE_SECONDS: i64 = 35 * 60;
// Per bot, over a conservative rolling 26-hour window; reserves shared quota.
const READ_LIMIT: u64 = 9_000;
const WRITE_LIMIT: u64 = 4_500;
const TABLES: &[&str] = &["deals", "subscriptions", "settings"];

pub struct CloudState {
    http: Client,
    base: String,
    name: String,
    identity: String,
    token: Option<(String, Instant)>,
    owner: String,
    manifest: Value,
    update_time: Option<String>,
    cached_hashes: Vec<String>,
    active: bool,
    poisoned: bool,
    changes: u64,
    session_start: Option<Instant>,
    runtime_day: i64,
    runtime_limit: u64,
    snapshot_reads: u64,
}
impl CloudState {
    pub fn new(project: &str, namespace: &str, identity: &str) -> Result<Self> {
        ensure!(
            valid_component(project) && valid_component(namespace),
            "invalid Firestore project or namespace"
        );
        ensure!(!identity.is_empty(), "cloud state identity is required");
        let root = format!("projects/{project}/databases/(default)/documents");
        Ok(Self {
            http: Client::builder()
                .no_proxy()
                .use_preconfigured_tls(
                    rustls::ClientConfig::builder_with_provider(std::sync::Arc::new(
                        rustls::crypto::ring::default_provider(),
                    ))
                    .with_safe_default_protocol_versions()?
                    .with_root_certificates(rustls::RootCertStore::from_iter(
                        webpki_roots::TLS_SERVER_ROOTS.iter().cloned(),
                    ))
                    .with_no_client_auth(),
                )
                .timeout(Duration::from_secs(15))
                .connect_timeout(Duration::from_secs(3))
                .redirect(reqwest::redirect::Policy::none())
                .build()?,
            base: format!("https://firestore.googleapis.com/v1/{root}"),
            name: format!("{root}/bot_checkpoints/{namespace}"),
            identity: identity.into(),
            token: None,
            owner: hex::encode(rand::random::<[u8; 16]>()),
            manifest: json!({}),
            update_time: None,
            cached_hashes: Vec::new(),
            active: false,
            poisoned: false,
            changes: 0,
            session_start: None,
            runtime_day: 0,
            runtime_limit: RUNTIME_LIMIT,
            snapshot_reads: 0,
        })
    }
    pub fn commands_only(mut self) -> Self {
        self.runtime_limit = 20_000;
        self
    }
    // Reads a subscription checkpoint without taking the poller's long lease.
    // Atomic commits plus a second manifest/hash check prevent mixed snapshots.
    // Unchanged checkpoints need only one small manifest read.
    pub fn read_snapshot(&mut self) -> Result<Option<Vec<u8>>> {
        self.snapshot_reads = 0;
        for _ in 0..3 {
            let name = self.name.clone();
            self.snapshot_reads += 1;
            let doc = self
                .get(&name)?
                .context("subscription checkpoint missing; import before polling")?;
            let manifest: Value = serde_json::from_str(
                doc["fields"]["payload"]["stringValue"]
                    .as_str()
                    .context("subscription manifest missing")?,
            )?;
            ensure!(
                manifest["format"] == 1 && manifest["identity"] == self.identity,
                "subscription checkpoint identity mismatch"
            );
            let hashes: Vec<String> = serde_json::from_value(manifest["hashes"].clone())?;
            ensure!(
                hashes.len() <= MAX_CHUNKS,
                "subscription checkpoint too large"
            );
            if hashes == self.cached_hashes {
                return Ok(None);
            }
            let mut compressed = Vec::new();
            let mut changed = false;
            for (i, hash) in hashes.iter().enumerate() {
                self.snapshot_reads += 1;
                let Some(chunk) = self.get(&format!("{}/bot_checkpoint_chunks/{i}", self.name))?
                else {
                    changed = true;
                    break;
                };
                let bytes = STANDARD.decode(
                    chunk["fields"]["data"]["bytesValue"]
                        .as_str()
                        .context("subscription chunk missing")?,
                )?;
                if bytes.len() > CHUNK || hex::encode(Sha256::digest(&bytes)) != *hash {
                    changed = true;
                    break;
                }
                compressed.extend_from_slice(&bytes);
            }
            if changed {
                continue;
            }
            self.snapshot_reads += 1;
            let latest = self
                .get(&name)?
                .context("subscription checkpoint vanished")?;
            let latest: Value = serde_json::from_str(
                latest["fields"]["payload"]["stringValue"]
                    .as_str()
                    .context("subscription manifest missing")?,
            )?;
            if latest["hashes"] != manifest["hashes"] {
                continue;
            }
            let mut raw = Vec::new();
            if compressed.is_empty() {
                raw = serde_json::to_vec(
                    &TABLES
                        .iter()
                        .map(|t| (*t, Vec::<Vec<Value>>::new()))
                        .collect::<BTreeMap<_, _>>(),
                )?;
            } else {
                GzDecoder::new(compressed.as_slice())
                    .take(MAX_RAW as u64 + 1)
                    .read_to_end(&mut raw)?;
            }
            ensure!(
                raw.len() <= MAX_RAW,
                "subscription checkpoint raw size exceeded"
            );
            self.cached_hashes = hashes;
            return Ok(Some(raw));
        }
        bail!("subscription snapshot changed repeatedly or is corrupt")
    }
    fn token(&mut self) -> Result<String> {
        if let Some((token, until)) = &self.token
            && Instant::now() < *until
        {
            return Ok(token.clone());
        }
        // Runtime identity; no service-account key, gcloud, or token in image/env.
        let r = self.http.get("http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token")
            .header("Metadata-Flavor", "Google").send()?.error_for_status()?;
        let v: Value = r.json()?;
        let token = v["access_token"]
            .as_str()
            .context("metadata access token missing")?
            .to_owned();
        let seconds = v["expires_in"]
            .as_u64()
            .unwrap_or(300)
            .saturating_sub(60)
            .max(1);
        self.token = Some((token.clone(), Instant::now() + Duration::from_secs(seconds)));
        Ok(token)
    }
    fn request(
        &mut self,
        method: Method,
        url: &str,
        body: Option<Value>,
        missing_ok: bool,
    ) -> Result<Option<Value>> {
        let token = self.token()?;
        let mut req = self.http.request(method, url).bearer_auth(token);
        if let Some(body) = body {
            req = req.json(&body);
        }
        let r = req.send()?;
        let status = r.status();
        if missing_ok && status == reqwest::StatusCode::NOT_FOUND {
            return Ok(None);
        }
        // Never include tokens or remote document bodies in errors/logs.
        ensure!(status.is_success(), "Firestore request failed: {status}");
        let mut bytes = Vec::new();
        r.take(12 * 1024 * 1024 + 1).read_to_end(&mut bytes)?;
        ensure!(
            bytes.len() <= 12 * 1024 * 1024,
            "Firestore response too large"
        );
        Ok(Some(serde_json::from_slice(&bytes)?))
    }
    fn get(&mut self, name: &str) -> Result<Option<Value>> {
        let suffix = name
            .split_once("/documents/")
            .context("invalid document name")?
            .1;
        self.request(Method::GET, &format!("{}/{suffix}", self.base), None, true)
    }
    fn commit(&mut self, mut writes: Vec<Value>, manifest: Value) -> Result<()> {
        let condition = self
            .update_time
            .as_ref()
            .map_or_else(|| json!({"exists":false}), |t| json!({"updateTime":t}));
        writes.push(json!({"update":{"name":self.name,"fields":{"payload":{"stringValue":serde_json::to_string(&manifest)?}}},"currentDocument":condition}));
        let manifest_index = writes.len() - 1;
        let v = self
            .request(
                Method::POST,
                &format!("{}:commit", self.base),
                Some(json!({"writes":writes})),
                false,
            )?
            .context("empty commit response")?;
        self.update_time = Some(
            v["writeResults"][manifest_index]["updateTime"]
                .as_str()
                .context("commit update time missing")?
                .into(),
        );
        self.manifest = manifest;
        Ok(())
    }
    fn budget(manifest: &mut Value, reads: u64, writes: u64) -> Result<()> {
        let hour = chrono::Utc::now().timestamp().div_euclid(3600);
        let mut buckets: BTreeMap<i64, (u64, u64)> = manifest["usage"]
            .as_object()
            .into_iter()
            .flat_map(|o| o.iter())
            .filter_map(|(h, n)| Some((h.parse::<i64>().ok()?, (n[0].as_u64()?, n[1].as_u64()?))))
            .filter(|(h, _)| *h >= hour - 26)
            .collect();
        let total = buckets
            .values()
            .fold((reads, writes), |(r, w), (a, b)| (r + a, w + b));
        ensure!(
            total.0 <= READ_LIMIT && total.1 <= WRITE_LIMIT,
            "cloud state operation budget reached; retry after usage ages out"
        );
        let b = buckets.entry(hour).or_default();
        b.0 += reads;
        b.1 += writes;
        manifest["usage"] = json!(
            buckets
                .into_iter()
                .map(|(h, n)| (h.to_string(), json!([n.0, n.1])))
                .collect::<BTreeMap<_, _>>()
        );
        Ok(())
    }
    fn reserve_runtime(manifest: &mut Value) -> Result<i64> {
        let day = chrono::Utc::now().timestamp().div_euclid(86400);
        let mut history: BTreeMap<i64, u64> = manifest["runtime"]
            .as_object()
            .into_iter()
            .flat_map(|o| o.iter())
            .filter_map(|(d, n)| Some((d.parse().ok()?, n.as_u64()?)))
            .filter(|(d, _)| *d >= day - 32)
            .collect();
        ensure!(
            history.values().sum::<u64>() + REQUEST_RESERVE <= RUNTIME_LIMIT,
            "cloud runtime allowance reached; polling paused until usage ages out"
        );
        *history.entry(day).or_default() += REQUEST_RESERVE;
        manifest["runtime"] = json!(
            history
                .into_iter()
                .map(|(d, n)| (d.to_string(), n))
                .collect::<BTreeMap<_, _>>()
        );
        Ok(day)
    }
    pub fn begin(&mut self, db: &mut Connection) -> Result<()> {
        // Always re-read the manifest. A previous request may have been cancelled.
        self.active = false;
        self.session_start = Some(Instant::now());
        let name = self.name.clone();
        let remote = self.get(&name)?;
        let mut manifest = if let Some(doc) = remote {
            self.update_time = Some(
                doc["updateTime"]
                    .as_str()
                    .context("manifest update time missing")?
                    .into(),
            );
            serde_json::from_str(
                doc["fields"]["payload"]["stringValue"]
                    .as_str()
                    .context("manifest payload missing")?,
            )?
        } else {
            self.update_time = None;
            json!({"format":1,"identity":self.identity,"hashes":[]})
        };
        ensure!(
            manifest["format"] == 1 && manifest["identity"] == self.identity,
            "cloud state belongs to a different bot/application or format"
        );
        let now = chrono::Utc::now().timestamp();
        ensure!(
            manifest["expires"].as_i64().unwrap_or(0) <= now
                || manifest["owner"].as_str() == Some(&self.owner),
            "cloud state is leased by another request"
        );
        let hashes: Vec<String> = serde_json::from_value(manifest["hashes"].clone())?;
        ensure!(
            hashes.len() <= MAX_CHUNKS,
            "cloud checkpoint chunk limit exceeded"
        );
        let reload = self.poisoned || hashes != self.cached_hashes;
        Self::budget(
            &mut manifest,
            1 + if reload { hashes.len() as u64 } else { 0 },
            1,
        )?;
        // Commands have a separate, small allowance; polling retains its budget.
        let day = chrono::Utc::now().timestamp().div_euclid(86400);
        let used = manifest["runtime"]
            .as_object()
            .into_iter()
            .flat_map(|o| o.iter())
            .filter_map(|(d, n)| Some((d.parse::<i64>().ok()?, n.as_u64()?)))
            .filter(|(d, _)| *d >= day - 32)
            .map(|(_, n)| n)
            .sum::<u64>();
        ensure!(
            used + REQUEST_RESERVE <= self.runtime_limit,
            "cloud runtime allowance reached"
        );
        self.runtime_day = Self::reserve_runtime(&mut manifest)?;
        manifest["owner"] = json!(self.owner);
        manifest["expires"] = json!(now + LEASE_SECONDS);
        self.commit(Vec::new(), manifest)?;
        self.active = true;
        self.poisoned = true;
        // The exclusive lease keeps chunks stable while they are being read.
        if reload {
            let mut compressed = Vec::new();
            for (i, hash) in hashes.iter().enumerate() {
                let name = format!("{}/bot_checkpoint_chunks/{i}", self.name);
                let doc = self.get(&name)?.context("checkpoint chunk missing")?;
                let data = STANDARD.decode(
                    doc["fields"]["data"]["bytesValue"]
                        .as_str()
                        .context("checkpoint bytes missing")?,
                )?;
                ensure!(
                    data.len() <= CHUNK && hex::encode(Sha256::digest(&data)) == *hash,
                    "checkpoint chunk integrity failure"
                );
                compressed.extend_from_slice(&data);
            }
            if compressed.is_empty() {
                restore(
                    db,
                    &serde_json::to_vec(
                        &TABLES
                            .iter()
                            .map(|t| (*t, Vec::<Vec<Value>>::new()))
                            .collect::<BTreeMap<_, _>>(),
                    )?,
                )?;
            } else {
                let mut raw = Vec::new();
                GzDecoder::new(compressed.as_slice())
                    .take(MAX_RAW as u64 + 1)
                    .read_to_end(&mut raw)?;
                ensure!(
                    raw.len() <= MAX_RAW,
                    "checkpoint decompressed size exceeded"
                );
                restore(db, &raw)?;
            }
        }
        self.cached_hashes = hashes;
        self.changes = db.total_changes();
        self.poisoned = false;
        self.active = true;
        Ok(())
    }
    pub fn ready(&self) -> Result<()> {
        ensure!(
            self.active
                && !self.poisoned
                && self.manifest["expires"].as_i64().unwrap_or(0)
                    > chrono::Utc::now().timestamp() + 30,
            "cloud state lease inactive, expired, or checkpoint failed"
        );
        Ok(())
    }
    pub fn checkpoint(&mut self, db: &Connection) -> Result<()> {
        self.ready()?;
        if db.total_changes() == self.changes {
            return Ok(());
        }
        let result = self.save(db);
        if result.is_err() {
            self.poisoned = true;
        }
        result
    }
    fn save(&mut self, db: &Connection) -> Result<()> {
        let raw = dump(db)?;
        ensure!(raw.len() <= MAX_RAW, "cloud checkpoint raw size exceeded");
        let mut gzip = GzEncoder::new(Vec::new(), Compression::fast());
        gzip.write_all(&raw)?;
        let compressed = gzip.finish()?;
        ensure!(
            compressed.len() <= CHUNK * MAX_CHUNKS,
            "cloud checkpoint compressed size exceeded"
        );
        let mut hashes = Vec::new();
        let mut writes = Vec::new();
        for (i, data) in compressed.chunks(CHUNK).enumerate() {
            let hash = hex::encode(Sha256::digest(data));
            if self.cached_hashes.get(i) != Some(&hash) {
                writes.push(json!({"update":{"name":format!("{}/bot_checkpoint_chunks/{i}",self.name),"fields":{"data":{"bytesValue":STANDARD.encode(data)}}}}));
            }
            hashes.push(hash);
        }
        for i in hashes.len()..self.cached_hashes.len() {
            writes.push(json!({"delete":format!("{}/bot_checkpoint_chunks/{i}",self.name)}));
        }
        let mut manifest = self.manifest.clone();
        Self::budget(&mut manifest, 0, writes.len() as u64 + 1)?;
        manifest["hashes"] = json!(hashes);
        manifest["raw_bytes"] = json!(raw.len());
        manifest["compressed_bytes"] = json!(compressed.len());
        self.commit(writes, manifest)?;
        self.cached_hashes = hashes;
        self.changes = db.total_changes();
        Ok(())
    }
    pub fn end(&mut self, db: &Connection) -> Result<()> {
        let result = (|| {
            if !self.active {
                return Ok(());
            }
            if !self.poisoned {
                self.checkpoint(db)?;
            }
            let mut manifest = self.manifest.clone();
            Self::budget(&mut manifest, 0, 1)?;
            // Include a conservative startup/response allowance. A cancelled or
            // crashed request keeps its full reservation, rather than refunding it.
            let charged = self
                .session_start
                .map(|t| t.elapsed().as_secs() + 11)
                .unwrap_or(REQUEST_RESERVE)
                .min(REQUEST_RESERVE);
            let day = self.runtime_day.to_string();
            if let Some(reserved) = manifest["runtime"][&day].as_u64() {
                manifest["runtime"][&day] =
                    json!(reserved.saturating_sub(REQUEST_RESERVE - charged));
            }
            manifest["owner"] = json!("");
            manifest["expires"] = json!(0);
            self.commit(Vec::new(), manifest)
        })();
        self.active = false;
        if result.is_err() {
            self.poisoned = true;
        }
        result
    }
}
fn valid_component(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 100
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_'))
}
pub fn dump(db: &Connection) -> Result<Vec<u8>> {
    let mut tables = BTreeMap::new();
    for &table in TABLES {
        let mut stmt = db.prepare(&format!("SELECT * FROM {table} ORDER BY rowid"))?;
        let width = stmt.column_count();
        let rows = stmt.query_map([], |r| {
            (0..width)
                .map(|i| r.get::<_, SqlValue>(i))
                .collect::<rusqlite::Result<Vec<_>>>()
        })?;
        let mut out = Vec::new();
        for row in rows {
            out.push(
                row?.into_iter()
                    .map(|v| match v {
                        SqlValue::Null => Ok(Value::Null),
                        SqlValue::Integer(v) => Ok(json!(v)),
                        SqlValue::Real(v) => Ok(json!(v)),
                        SqlValue::Text(v) => Ok(json!(v)),
                        SqlValue::Blob(_) => bail!("unexpected blob in bot schema"),
                    })
                    .collect::<Result<Vec<_>>>()?,
            );
        }
        tables.insert(table, out);
    }
    Ok(serde_json::to_vec(&tables)?)
}
pub fn restore(db: &mut Connection, raw: &[u8]) -> Result<()> {
    let tables: BTreeMap<String, Vec<Vec<Value>>> = serde_json::from_slice(raw)?;
    ensure!(
        tables.len() == TABLES.len() && TABLES.iter().all(|t| tables.contains_key(*t)),
        "foreign checkpoint schema"
    );
    let tx = db.transaction()?;
    for &table in TABLES {
        tx.execute(&format!("DELETE FROM {table}"), [])?;
        for row in &tables[table] {
            let values = row
                .iter()
                .map(|v| match v {
                    Value::Null => Ok(SqlValue::Null),
                    Value::String(s) => Ok(SqlValue::Text(s.clone())),
                    Value::Number(n) => n
                        .as_i64()
                        .map(SqlValue::Integer)
                        .or_else(|| n.as_f64().map(SqlValue::Real))
                        .context("invalid SQL number"),
                    _ => bail!("invalid SQL checkpoint cell"),
                })
                .collect::<Result<Vec<_>>>()?;
            let marks = vec!["?"; row.len()].join(",");
            tx.execute(
                &format!("INSERT INTO {table} VALUES({marks})"),
                rusqlite::params_from_iter(values),
            )?;
        }
    }
    tx.commit()?;
    Ok(())
}

impl crate::storage::Store {
    pub fn cloud_begin(&mut self) -> Result<()> {
        let mut cloud = self
            .cloud
            .take()
            .context("Firestore state is not configured")?;
        let result = cloud.begin(&mut self.db);
        self.cloud = Some(cloud);
        result
    }
    pub fn cloud_sync_subscriptions(&mut self) -> Result<()> {
        let mut remote = self
            .cloud_subscriptions
            .take()
            .context("subscription state is not configured")?;
        let result = remote.read_snapshot();
        let reads = remote.snapshot_reads;
        // Put the HTTP client back even when reading or decoding fails.
        self.cloud_subscriptions = Some(remote);
        if let Some(cloud) = &mut self.cloud {
            CloudState::budget(&mut cloud.manifest, reads, 0)?;
        }
        let applied = (|| -> Result<()> {
            if let Some(raw) = result? {
                let mut temporary = Self::open(std::path::Path::new(":memory:"))?;
                restore(&mut temporary.db, &raw)?;
                let subs = temporary.subscriptions(None)?;
                if serde_json::to_vec(&subs)? != serde_json::to_vec(&self.subscriptions(None)?)? {
                    self.db.execute_batch("BEGIN IMMEDIATE")?;
                    let result = (|| -> Result<()> {
                        self.db.execute("DELETE FROM subscriptions", [])?;
                        for sub in &subs {
                            self.save_subscription(sub)?;
                        }
                        Ok(())
                    })();
                    match result {
                        Ok(()) => self.db.execute_batch("COMMIT")?,
                        Err(error) => {
                            self.db.execute_batch("ROLLBACK")?;
                            return Err(error);
                        }
                    }
                }
            }
            Ok(())
        })();
        if applied.is_err()
            && let Some(remote) = &mut self.cloud_subscriptions
        {
            remote.cached_hashes.clear();
        }
        applied
    }
    pub fn cloud_end(&mut self) -> Result<()> {
        let mut cloud = self
            .cloud
            .take()
            .context("Firestore state is not configured")?;
        let result = cloud.end(&self.db);
        self.cloud = Some(cloud);
        result
    }
    pub(crate) fn cloud_ready(&self) -> Result<()> {
        if let Some(cloud) = &self.cloud {
            cloud.ready()?;
        }
        Ok(())
    }
    pub(crate) fn cloud_checkpoint(&mut self) -> Result<()> {
        if let Some(cloud) = &mut self.cloud {
            cloud.checkpoint(&self.db)?;
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        net::{TcpListener, TcpStream},
        sync::{
            Arc, Mutex,
            atomic::{AtomicBool, Ordering},
        },
    };
    #[derive(Default)]
    struct Remote {
        docs: BTreeMap<String, Value>,
        revision: u64,
        fail_after_commit: bool,
    }
    struct Emulator {
        base: String,
        data: Arc<Mutex<Remote>>,
        stop: Arc<AtomicBool>,
        thread: Option<std::thread::JoinHandle<()>>,
    }
    impl Emulator {
        fn new() -> Self {
            let listener = TcpListener::bind("127.0.0.1:0").unwrap();
            let address = listener.local_addr().unwrap();
            listener.set_nonblocking(true).unwrap();
            let data = Arc::new(Mutex::new(Remote::default()));
            let state = data.clone();
            let stop = Arc::new(AtomicBool::new(false));
            let stopped = stop.clone();
            let thread = std::thread::spawn(move || {
                while !stopped.load(Ordering::Relaxed) {
                    match listener.accept() {
                        Ok((mut socket, _)) => respond(&mut socket, &state),
                        Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                            std::thread::sleep(Duration::from_millis(2))
                        }
                        Err(e) => panic!("fixture listener: {e}"),
                    }
                }
            });
            Self {
                base: format!("http://{address}/v1/projects/test/databases/(default)/documents"),
                data,
                stop,
                thread: Some(thread),
            }
        }
        fn client(&self, identity: &str) -> CloudState {
            let mut c = CloudState::new("test", "unit-fixture", identity).unwrap();
            c.base = self.base.clone();
            c.token = Some((
                "fixture-only".into(),
                Instant::now() + Duration::from_secs(3600),
            ));
            c
        }
    }
    impl Drop for Emulator {
        fn drop(&mut self) {
            self.stop.store(true, Ordering::Relaxed);
            self.thread.take().unwrap().join().unwrap();
        }
    }
    fn respond(socket: &mut TcpStream, state: &Arc<Mutex<Remote>>) {
        socket
            .set_read_timeout(Some(Duration::from_secs(3)))
            .unwrap();
        let mut bytes = Vec::new();
        let mut byte = [0];
        while !bytes.ends_with(b"\r\n\r\n") {
            if socket.read_exact(&mut byte).is_err() {
                return;
            }
            bytes.push(byte[0]);
        }
        let header = String::from_utf8(bytes).unwrap();
        let first = header.lines().next().unwrap();
        let parts: Vec<_> = first.split_whitespace().collect();
        let length = header
            .lines()
            .find_map(|l| {
                l.to_ascii_lowercase()
                    .strip_prefix("content-length:")
                    .map(|v| v.trim().parse::<usize>().unwrap())
            })
            .unwrap_or(0);
        let mut body = vec![0; length];
        socket.read_exact(&mut body).unwrap();
        let mut state = state.lock().unwrap();
        let (status, result) = if parts[0] == "GET" {
            let name = parts[1].trim_start_matches("/v1/");
            state
                .docs
                .get(name)
                .map_or((404, json!({})), |v| (200, v.clone()))
        } else {
            let v: Value = serde_json::from_slice(&body).unwrap();
            let writes = v["writes"].as_array().unwrap();
            let valid = writes.iter().all(|w| {
                let name = w["update"]["name"].as_str().unwrap_or("");
                if let Some(time) = w["currentDocument"]["updateTime"].as_str() {
                    state
                        .docs
                        .get(name)
                        .is_some_and(|d| d["updateTime"] == time)
                } else if w["currentDocument"]["exists"] == false {
                    !state.docs.contains_key(name)
                } else {
                    true
                }
            });
            if !valid {
                (409, json!({}))
            } else {
                state.revision += 1;
                let time = format!("rev-{}", state.revision);
                let mut results = Vec::new();
                for w in writes {
                    if let Some(name) = w["delete"].as_str() {
                        state.docs.remove(name);
                    } else {
                        let mut doc = w["update"].clone();
                        doc["updateTime"] = json!(time);
                        state.docs.insert(doc["name"].as_str().unwrap().into(), doc);
                    }
                    results.push(json!({"updateTime":time}));
                }
                let fail = std::mem::take(&mut state.fail_after_commit);
                (
                    if fail { 503 } else { 200 },
                    json!({"writeResults":results}),
                )
            }
        };
        let body = serde_json::to_vec(&result).unwrap();
        write!(socket,"HTTP/1.1 {status} Fixture\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",body.len()).unwrap();
        socket.write_all(&body).unwrap();
    }
    fn seed(db: &Connection, value: &str) {
        db.execute("INSERT INTO settings(key,payload) VALUES('unit',?) ON CONFLICT(key) DO UPDATE SET payload=excluded.payload",[value]).unwrap();
    }
    fn value(db: &Connection) -> String {
        db.query_row("SELECT payload FROM settings WHERE key='unit'", [], |r| {
            r.get(0)
        })
        .unwrap()
    }
    #[test]
    fn checkpoint_survives_a_fresh_instance_and_noop_calls_do_not_write() {
        let remote = Emulator::new();
        let mut a = remote.client("rfd:test:1");
        let mut db = crate::storage::Store::open(std::path::Path::new(":memory:"))
            .unwrap()
            .db;
        a.begin(&mut db).unwrap();
        seed(&db, "receipt");
        a.checkpoint(&db).unwrap();
        let revision = remote.data.lock().unwrap().revision;
        a.checkpoint(&db).unwrap();
        assert_eq!(remote.data.lock().unwrap().revision, revision);
        a.end(&db).unwrap();
        let mut b = remote.client("rfd:test:1");
        let mut fresh = crate::storage::Store::open(std::path::Path::new(":memory:"))
            .unwrap()
            .db;
        b.begin(&mut fresh).unwrap();
        assert_eq!(value(&fresh), "receipt");
        b.end(&fresh).unwrap();
    }
    #[test]
    fn conflicting_lease_and_stale_writer_are_rejected() {
        let remote = Emulator::new();
        let mut a = remote.client("rfd:test:1");
        let mut b = remote.client("rfd:test:1");
        let mut first = crate::storage::Store::open(std::path::Path::new(":memory:"))
            .unwrap()
            .db;
        let mut second = crate::storage::Store::open(std::path::Path::new(":memory:"))
            .unwrap()
            .db;
        a.begin(&mut first).unwrap();
        assert!(b.begin(&mut second).is_err());
        {
            let mut state = remote.data.lock().unwrap();
            let doc = state.docs.get_mut(&a.name).unwrap();
            let mut payload: Value =
                serde_json::from_str(doc["fields"]["payload"]["stringValue"].as_str().unwrap())
                    .unwrap();
            payload["expires"] = json!(0);
            doc["fields"]["payload"]["stringValue"] = json!(payload.to_string());
        }
        b.begin(&mut second).unwrap();
        seed(&first, "stale");
        assert!(a.checkpoint(&first).is_err());
        assert!(a.ready().is_err());
        seed(&second, "winner");
        b.checkpoint(&second).unwrap();
        b.end(&second).unwrap();
        a.begin(&mut first).unwrap();
        assert_eq!(value(&first), "winner");
        a.end(&first).unwrap();
    }
    #[test]
    fn ambiguous_commit_is_reloaded_before_any_more_work() {
        let remote = Emulator::new();
        let mut c = remote.client("rfd:test:1");
        let mut db = crate::storage::Store::open(std::path::Path::new(":memory:"))
            .unwrap()
            .db;
        c.begin(&mut db).unwrap();
        seed(&db, "acknowledged");
        remote.data.lock().unwrap().fail_after_commit = true;
        assert!(c.checkpoint(&db).is_err());
        assert!(c.ready().is_err());
        c.begin(&mut db).unwrap();
        assert_eq!(value(&db), "acknowledged");
        c.end(&db).unwrap();
    }
    #[test]
    fn foreign_identity_and_corrupt_chunks_fail_closed() {
        let remote = Emulator::new();
        let mut c = remote.client("rfd:test:1");
        let mut db = crate::storage::Store::open(std::path::Path::new(":memory:"))
            .unwrap()
            .db;
        c.begin(&mut db).unwrap();
        seed(&db, "receipt");
        c.checkpoint(&db).unwrap();
        c.end(&db).unwrap();
        assert!(remote.client("another-bot:2").begin(&mut db).is_err());
        {
            let mut state = remote.data.lock().unwrap();
            state
                .docs
                .get_mut(&format!("{}/bot_checkpoint_chunks/0", c.name))
                .unwrap()["fields"]["data"]["bytesValue"] = json!(STANDARD.encode(b"corrupt"));
        }
        assert!(remote.client("rfd:test:1").begin(&mut db).is_err());
        assert_eq!(value(&db), "receipt");
    }
    #[test]
    fn restore_is_atomic_and_operation_budget_is_persistent() {
        let mut db = crate::storage::Store::open(std::path::Path::new(":memory:"))
            .unwrap()
            .db;
        seed(&db, "original");
        let mut snapshot: Value = serde_json::from_slice(&dump(&db).unwrap()).unwrap();
        snapshot["settings"] = json!([["key", "new"], ["bad-width"]]);
        assert!(restore(&mut db, &serde_json::to_vec(&snapshot).unwrap()).is_err());
        assert_eq!(value(&db), "original");
        let mut manifest = json!({});
        CloudState::budget(&mut manifest, READ_LIMIT, WRITE_LIMIT).unwrap();
        assert!(CloudState::budget(&mut manifest, 1, 0).is_err());
        assert!(CloudState::budget(&mut manifest, 0, 1).is_err());
    }
    #[test]
    fn runtime_reservations_refund_completed_work_and_keep_crashed_work() {
        let remote = Emulator::new();
        let mut c = remote.client("rfd:test:1");
        let mut db = crate::storage::Store::open(std::path::Path::new(":memory:"))
            .unwrap()
            .db;
        c.begin(&mut db).unwrap();
        let day = c.runtime_day.to_string();
        assert_eq!(c.manifest["runtime"][&day], REQUEST_RESERVE);
        c.end(&db).unwrap();
        let charged = c.manifest["runtime"][&day].as_u64().unwrap();
        assert!((11..REQUEST_RESERVE).contains(&charged));
        c.begin(&mut db).unwrap(); // simulate losing the worker after acquiring
        assert_eq!(c.manifest["runtime"][&day], charged + REQUEST_RESERVE);
        c.begin(&mut db).unwrap();
        assert_eq!(c.manifest["runtime"][&day], charged + REQUEST_RESERVE * 2);
        c.end(&db).unwrap();
        assert!(c.manifest["runtime"][&day].as_u64().unwrap() >= REQUEST_RESERVE);
        let today = chrono::Utc::now().timestamp().div_euclid(86400);
        let mut manifest = json!({"runtime":{today.to_string():RUNTIME_LIMIT}});
        assert!(CloudState::reserve_runtime(&mut manifest).is_err());
        let mut old = json!({"runtime":{(today-33).to_string():RUNTIME_LIMIT}});
        CloudState::reserve_runtime(&mut old).unwrap();
        assert_eq!(old["runtime"][today.to_string()], REQUEST_RESERVE);
    }
    #[test]
    fn multiple_chunks_restore_exactly_and_shrinking_removes_obsolete_chunks() {
        let remote = Emulator::new();
        let mut c = remote.client("rfd:test:1");
        let mut db = crate::storage::Store::open(std::path::Path::new(":memory:"))
            .unwrap()
            .db;
        c.begin(&mut db).unwrap();
        let mut random = 91u64;
        let text: String = (0..CHUNK * 3)
            .map(|_| {
                random ^= random << 13;
                random ^= random >> 7;
                random ^= random << 17;
                char::from(b'!' + (random % 90) as u8)
            })
            .collect();
        seed(&db, &text);
        c.checkpoint(&db).unwrap();
        assert!(c.cached_hashes.len() > 1);
        c.end(&db).unwrap();
        let mut fresh = crate::storage::Store::open(std::path::Path::new(":memory:"))
            .unwrap()
            .db;
        let mut reader = remote.client("rfd:test:1");
        reader.begin(&mut fresh).unwrap();
        assert_eq!(value(&fresh), text);
        seed(&fresh, "small");
        reader.checkpoint(&fresh).unwrap();
        reader.end(&fresh).unwrap();
        assert_eq!(reader.cached_hashes.len(), 1);
        assert!(
            !remote
                .data
                .lock()
                .unwrap()
                .docs
                .contains_key(&format!("{}/bot_checkpoint_chunks/1", c.name))
        );
    }
    #[test]
    fn command_subscriptions_change_while_worker_is_leased_and_reload_after_restart() {
        let remote = Emulator::new();
        let mut commands = remote.client("rfd:test:1");
        commands.name = commands
            .name
            .replace("unit-fixture", "subscriptions-fixture");
        let mut command_store =
            crate::storage::Store::open(std::path::Path::new(":memory:")).unwrap();
        commands.begin(&mut command_store.db).unwrap();
        command_store
            .save_subscription(&crate::models::Subscription {
                guild_id: "1".into(),
                channel_id: "42".into(),
                deal_type: "rfd_all".into(),
                subscription_type: "rfd".into(),
                added_at: crate::time::Timestamp::now(),
                ..Default::default()
            })
            .unwrap();
        commands.checkpoint(&command_store.db).unwrap();
        commands.end(&command_store.db).unwrap();
        let mut store = crate::storage::Store::open(std::path::Path::new(":memory:")).unwrap();
        store.cloud = Some(remote.client("rfd:test:1"));
        let mut reader = remote.client("rfd:test:1");
        reader.name = commands.name.clone();
        store.cloud_subscriptions = Some(reader);
        store.cloud_begin().unwrap();
        store.cloud_sync_subscriptions().unwrap();
        store.cloud_checkpoint().unwrap();
        assert_eq!(store.subscriptions(None).unwrap().len(), 1);
        let revision = remote.data.lock().unwrap().revision;
        store.cloud_sync_subscriptions().unwrap();
        store.cloud_checkpoint().unwrap();
        assert_eq!(remote.data.lock().unwrap().revision, revision);
        // Independent command store remains writable during a long worker lease.
        commands.begin(&mut command_store.db).unwrap();
        command_store.remove_subscription("1", "42", None).unwrap();
        commands.checkpoint(&command_store.db).unwrap();
        commands.end(&command_store.db).unwrap();
        store.cloud_sync_subscriptions().unwrap();
        store.cloud_checkpoint().unwrap();
        assert!(store.subscriptions(None).unwrap().is_empty());
        store.cloud_end().unwrap();
        // Read-only subscriptions can be restored in an entirely fresh instance.
        let mut reader = remote.client("rfd:test:1");
        reader.name = commands.name.clone();
        let raw = reader.read_snapshot().unwrap().unwrap();
        let mut fresh = crate::storage::Store::open(std::path::Path::new(":memory:")).unwrap();
        restore(&mut fresh.db, &raw).unwrap();
        assert!(fresh.subscriptions(None).unwrap().is_empty());
    }
    #[test]
    fn restoring_receipts_rebuilds_derived_thread_aliases() {
        let mut source = crate::storage::Store::open(std::path::Path::new(":memory:")).unwrap();
        source.bind_application("1001").unwrap();
        let payload = r#"{"DocumentID":"canonical","Threads":[{"DocumentID":"alias"}],"DiscordMessageIDs":{"42":"receipt"},"DiscordMessageApplicationIDs":{"42":"1001"}}"#;
        source
            .db
            .execute("INSERT INTO deals VALUES('canonical',?,0,0)", [payload])
            .unwrap();
        let raw = dump(&source.db).unwrap();
        let mut target = crate::storage::Store::open(std::path::Path::new(":memory:")).unwrap();
        restore(&mut target.db, &raw).unwrap();
        let aliases: String = target
            .db
            .query_row(
                "SELECT deal_id FROM deal_threads WHERE thread_id='alias'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(aliases, "canonical");
        assert_eq!(
            target
                .deal("canonical")
                .unwrap()
                .unwrap()
                .discord_message_ids["42"],
            "receipt"
        );
        target.bind_application("1001").unwrap();
    }
}
