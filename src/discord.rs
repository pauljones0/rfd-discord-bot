//! Small Discord REST/Gateway client: no guild/member cache or message intents.
use crate::http;
use anyhow::{Result, bail, ensure};
use futures_util::{FutureExt, SinkExt, StreamExt, future::BoxFuture};
use reqwest::{Client, Method};
use serde_json::{Value, json};
use std::{
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicI64, AtomicU64, Ordering},
    },
    time::Duration,
};
use tokio::{
    sync::{Mutex, watch},
    task::JoinSet,
    time::{Instant, sleep_until},
};
use tokio_tungstenite::tungstenite::{Message, protocol::WebSocketConfig};

#[derive(Clone)]
pub struct Discord {
    client: Client,
    token: Arc<str>,
    pub application_id: Arc<str>,
    base: Arc<str>,
    next_message: Arc<Mutex<Instant>>,
    global_until: Arc<Mutex<Instant>>,
}
impl Discord {
    pub fn new(token: String, app: String) -> Result<Self> {
        Self::with_base(token, app, "https://discord.com/api/v10".into())
    }
    pub fn with_base(token: String, app: String, base: String) -> Result<Self> {
        let mut client = http::client(Duration::from_secs(10), false, Some(Vec::new()))?;
        // Empty redirect allowlist means REST credentials can never follow a redirect.
        let _ = &mut client;
        Ok(Self {
            client,
            token: token.into(),
            application_id: app.into(),
            base: base.trim_end_matches('/').into(),
            next_message: Arc::new(Mutex::new(Instant::now())),
            global_until: Arc::new(Mutex::new(Instant::now())),
        })
    }
    pub async fn request(
        &self,
        method: Method,
        path: &str,
        payload: Option<&Value>,
        message: bool,
    ) -> Result<Value> {
        ensure!(
            !self.token.trim().is_empty(),
            "DISCORD_BOT_TOKEN is required"
        );
        ensure!(
            path.starts_with('/') && !path.starts_with("//"),
            "invalid Discord API path"
        );
        for attempt in 0..4u32 {
            let global = *self.global_until.lock().await;
            sleep_until(global).await;
            if message {
                let mut next = self.next_message.lock().await;
                sleep_until(*next).await;
                *next = Instant::now() + Duration::from_millis(1200);
            }
            let mut req = self
                .client
                .request(method.clone(), format!("{}{path}", self.base))
                .header("Authorization", format!("Bot {}", self.token))
                .header(
                    reqwest::header::USER_AGENT,
                    concat!(
                        "DiscordBot (https://github.com/pauljones0, ",
                        env!("CARGO_PKG_VERSION"),
                        ")"
                    ),
                );
            if let Some(value) = payload {
                req = req.json(value);
            }
            let response = req.send().await;
            let mut delay = Duration::from_secs(1u64 << attempt);
            match response {
                Err(_) => {
                    if attempt == 3 {
                        bail!("Discord transport request failed");
                    }
                }
                Ok(response) => {
                    let status = response.status();
                    let headers = response.headers().clone();
                    let raw = http::body(response, 1024 * 1024)
                        .await
                        .map_err(|_| anyhow::anyhow!("could not read Discord response"))?;
                    if status.is_success() {
                        return if raw.is_empty() {
                            Ok(Value::Null)
                        } else {
                            serde_json::from_slice(&raw)
                                .map_err(|_| anyhow::anyhow!("invalid Discord response JSON"))
                        };
                    }
                    if status.as_u16() != 429 && !status.is_server_error() {
                        bail!("Discord HTTP status {}", status.as_u16());
                    }
                    if attempt == 3 {
                        bail!("Discord HTTP status {} after 4 attempts", status.as_u16());
                    }
                    if status.as_u16() == 429 {
                        delay = delay.max(http::retry_after(&headers));
                        if let Ok(limit) = serde_json::from_slice::<Value>(&raw) {
                            if let Some(seconds) = limit["retry_after"]
                                .as_f64()
                                .filter(|s| s.is_finite() && *s > 0.0)
                            {
                                delay = delay.max(Duration::from_secs_f64(seconds.min(120.0)));
                            }
                            if limit["global"] == true {
                                *self.global_until.lock().await = Instant::now() + delay;
                            }
                        }
                    }
                }
            }
            tokio::time::sleep(delay).await;
        }
        bail!("Discord request failed")
    }
    pub async fn check_application(&self) -> Result<()> {
        let app = self
            .request(Method::GET, "/applications/@me", None, false)
            .await?;
        ensure!(
            app["id"].as_str() == Some(&self.application_id),
            "DISCORD_APP_ID does not match this bot token"
        );
        ensure!(
            app["interactions_endpoint_url"]
                .as_str()
                .is_none_or(str::is_empty),
            "clear the Discord Interactions Endpoint URL to enable Gateway delivery"
        );
        Ok(())
    }
    pub async fn callback(&self, interaction: &Value, response: &Value) -> Result<()> {
        let id = interaction["id"]
            .as_str()
            .filter(|s| valid_id(s))
            .ok_or_else(|| anyhow::anyhow!("invalid interaction ID"))?;
        let token = interaction["token"]
            .as_str()
            .filter(|s| {
                !s.is_empty()
                    && s.bytes()
                        .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-' || b == b'.')
            })
            .ok_or_else(|| anyhow::anyhow!("invalid interaction token"))?;
        self.request(
            Method::POST,
            &format!("/interactions/{id}/{token}/callback"),
            Some(response),
            false,
        )
        .await?;
        Ok(())
    }
    pub async fn send(&self, channel: &str, payload: &Value) -> Result<String> {
        ensure!(valid_id(channel), "invalid channel ID");
        let value = self
            .request(
                Method::POST,
                &format!("/channels/{channel}/messages"),
                Some(payload),
                true,
            )
            .await?;
        let id = value["id"]
            .as_str()
            .filter(|s| valid_id(s))
            .ok_or_else(|| anyhow::anyhow!("Discord returned an invalid message receipt"))?;
        ensure!(
            value["channel_id"].as_str().is_none_or(|c| c == channel),
            "Discord returned the wrong channel receipt"
        );
        Ok(id.into())
    }
    pub async fn edit(&self, channel: &str, message: &str, payload: &Value) -> Result<()> {
        ensure!(
            valid_id(channel) && valid_id(message),
            "invalid message destination"
        );
        self.request(
            Method::PATCH,
            &format!("/channels/{channel}/messages/{message}"),
            Some(payload),
            true,
        )
        .await?;
        Ok(())
    }
}
pub fn valid_id(s: &str) -> bool {
    s.parse::<u64>().is_ok_and(|n| n > 0 && n.to_string() == s)
}
pub fn private_reply(content: impl Into<String>) -> Value {
    json!({"type":4,"data":{"content":content.into(),"flags":64,"allowed_mentions":{"parse":[]}}})
}
pub fn can_manage(req: &Value) -> bool {
    req["member"]["permissions"]
        .as_str()
        .and_then(|s| s.parse::<u64>().ok())
        .is_some_and(|p| p & (0x20 | 0x8) != 0)
}
pub type InteractionHandler = Arc<dyn Fn(Value) -> BoxFuture<'static, Value> + Send + Sync>;
#[derive(Default)]
pub struct GatewayHealth {
    pub ready: AtomicBool,
    pub received: AtomicU64,
    pub responded: AtomicU64,
    pub failed: AtomicU64,
    pub last_received: AtomicI64,
}
impl GatewayHealth {
    pub fn json(&self) -> Value {
        json!({"transport":"gateway","ready":self.ready.load(Ordering::Relaxed),"received":self.received.load(Ordering::Relaxed),"responded":self.responded.load(Ordering::Relaxed),"failed":self.failed.load(Ordering::Relaxed),"last_received_unix":self.last_received.load(Ordering::Relaxed)})
    }
}
#[derive(Default)]
struct Session {
    id: String,
    sequence: Option<i64>,
    resume_url: String,
}
pub struct Gateway {
    pub api: Discord,
    pub health: Arc<GatewayHealth>,
    handler: InteractionHandler,
}
impl Gateway {
    pub fn new(api: Discord, handler: InteractionHandler) -> Self {
        Self {
            api,
            health: Arc::new(GatewayHealth::default()),
            handler,
        }
    }
    pub async fn run(&self, mut stop: watch::Receiver<bool>) -> Result<()> {
        let mut session = Session::default();
        let mut tasks = JoinSet::new();
        let mut delay = Duration::from_secs(5);
        loop {
            if *stop.borrow() {
                break;
            }
            let result = tokio::select! {r=self.connection(&mut session,&mut tasks)=>r,_=stop.changed()=>break};
            self.health.ready.store(false, Ordering::Relaxed);
            match result {
                Ok(()) => delay = Duration::from_secs(5),
                Err(ConnectionError::Fatal(reason)) => return Err(anyhow::anyhow!(reason)),
                Err(ConnectionError::Retry) => {
                    tracing::warn!(
                        retry_seconds = delay.as_secs(),
                        "Discord Gateway disconnected"
                    );
                }
            }
            tokio::select! {_=tokio::time::sleep(delay)=>{},_=stop.changed()=>break};
            delay = (delay * 2).min(Duration::from_secs(60));
        }
        self.health.ready.store(false, Ordering::Relaxed);
        // Finish acknowledgements already in flight during graceful shutdown.
        let _ = tokio::time::timeout(Duration::from_secs(3), async {
            while tasks.join_next().await.is_some() {}
        })
        .await;
        tasks.abort_all();
        Ok(())
    }
    async fn connection(
        &self,
        session: &mut Session,
        tasks: &mut JoinSet<()>,
    ) -> std::result::Result<(), ConnectionError> {
        let discovery = self
            .api
            .request(Method::GET, "/gateway/bot", None, false)
            .await
            .map_err(|_| ConnectionError::Retry)?;
        if session.id.is_empty()
            && discovery["session_start_limit"]["remaining"].as_u64() == Some(0)
        {
            let reset = discovery["session_start_limit"]["reset_after"]
                .as_u64()
                .unwrap_or(60_000)
                .clamp(5000, 86_400_000);
            tracing::warn!(
                reset_ms = reset,
                "waiting for Discord Gateway session allowance"
            );
            tokio::time::sleep(Duration::from_millis(reset)).await;
            return Err(ConnectionError::Retry);
        }
        let base = if !session.id.is_empty() && !session.resume_url.is_empty() {
            session.resume_url.as_str()
        } else {
            discovery["url"].as_str().ok_or(ConnectionError::Retry)?
        };
        let mut url = url::Url::parse(base).map_err(|_| ConnectionError::Retry)?;
        let loopback_fixture = self.api.base.starts_with("http://127.0.0.1:")
            && url.scheme() == "ws"
            && url.host_str() == Some("127.0.0.1");
        if !loopback_fixture
            && (url.scheme() != "wss"
                || !url.host_str().is_some_and(|h| {
                    h == "discord.gg"
                        || h.ends_with(".discord.gg")
                        || h == "discord.com"
                        || h.ends_with(".discord.com")
                }))
        {
            return Err(ConnectionError::Fatal("invalid Discord Gateway URL"));
        }
        url.query_pairs_mut()
            .append_pair("v", "10")
            .append_pair("encoding", "json");
        let config = WebSocketConfig::default()
            .max_message_size(Some(1024 * 1024))
            .max_frame_size(Some(1024 * 1024));
        let (mut socket, _) = tokio::time::timeout(
            Duration::from_secs(20),
            tokio_tungstenite::connect_async_with_config(url.as_str(), Some(config), false),
        )
        .await
        .map_err(|_| ConnectionError::Retry)?
        .map_err(|_| ConnectionError::Retry)?;
        let hello = tokio::time::timeout(Duration::from_secs(20), socket.next())
            .await
            .map_err(|_| ConnectionError::Retry)?
            .ok_or(ConnectionError::Retry)?
            .map_err(|_| ConnectionError::Retry)?;
        let hello: Value =
            serde_json::from_slice(&hello.into_data()).map_err(|_| ConnectionError::Retry)?;
        if hello["op"] != 10 {
            return Err(ConnectionError::Retry);
        }
        let interval = hello["d"]["heartbeat_interval"]
            .as_u64()
            .filter(|n| *n > 0 && *n <= 300000)
            .ok_or(ConnectionError::Retry)?;
        let identify = if session.id.is_empty() {
            json!({"op":2,"d":{"token":self.api.token.as_ref(),"intents":0,"properties":{"os":"linux","browser":"rfd-rust","device":"rfd-rust"}}})
        } else {
            json!({"op":6,"d":{"token":self.api.token.as_ref(),"session_id":session.id,"seq":session.sequence}})
        };
        socket
            .send(Message::Text(identify.to_string().into()))
            .await
            .map_err(|_| ConnectionError::Retry)?;
        let mut heartbeat = tokio::time::interval_at(
            Instant::now() + Duration::from_millis(rand::random::<u64>() % interval),
            Duration::from_millis(interval),
        );
        heartbeat.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
        let mut awaiting_ack = false;
        let ready_deadline = Instant::now() + Duration::from_secs(20);
        loop {
            tokio::select! {
             _=sleep_until(ready_deadline),if !self.health.ready.load(Ordering::Relaxed)=>return Err(ConnectionError::Retry),
             _=heartbeat.tick()=>{if awaiting_ack{return Err(ConnectionError::Retry);}socket.send(Message::Text(json!({"op":1,"d":session.sequence}).to_string().into())).await.map_err(|_|ConnectionError::Retry)?;awaiting_ack=true;},
             _=tasks.join_next(),if !tasks.is_empty()=>{},
             incoming=socket.next()=>{
              let incoming=incoming.ok_or(ConnectionError::Retry)?.map_err(|_|ConnectionError::Retry)?;
              let raw=match incoming{Message::Text(text)=>text.as_bytes().to_vec(),Message::Binary(raw)=>raw.to_vec(),Message::Ping(_)=>{socket.flush().await.map_err(|_|ConnectionError::Retry)?;continue;},Message::Close(frame)=>{if let Some(frame)=frame{let code:u16=frame.code.into();if matches!(code,4007|4009){*session=Session::default();}else if matches!(code,4004|4010|4011|4012|4013|4014){return Err(ConnectionError::Fatal("Discord rejected Gateway configuration or credentials"));}}return Err(ConnectionError::Retry);},_=>continue};
              let value:Value=serde_json::from_slice(&raw).map_err(|_|ConnectionError::Retry)?;
              if let Some(sequence)=value["s"].as_i64(){session.sequence=Some(sequence);}
              match value["op"].as_u64(){
               Some(11)=>awaiting_ack=false,
               Some(1)=>{socket.send(Message::Text(json!({"op":1,"d":session.sequence}).to_string().into())).await.map_err(|_|ConnectionError::Retry)?;awaiting_ack=true;},
               Some(7)=>return Ok(()),
               Some(9)=>{if value["d"]!=true{*session=Session::default();}return Ok(());},
               Some(0)=>match value["t"].as_str(){
                Some("READY")=>{session.id=value["d"]["session_id"].as_str().ok_or(ConnectionError::Retry)?.into();session.resume_url=value["d"]["resume_gateway_url"].as_str().unwrap_or("").into();self.health.ready.store(true,Ordering::Relaxed);tracing::info!("Discord Gateway ready");},
                Some("RESUMED")=>{self.health.ready.store(true,Ordering::Relaxed);tracing::info!("Discord Gateway resumed");},
                Some("INTERACTION_CREATE")=>{
                 if !valid_interaction(&value["d"]){self.health.failed.fetch_add(1,Ordering::Relaxed);continue;}
                 self.health.received.fetch_add(1,Ordering::Relaxed);self.health.last_received.store(chrono::Utc::now().timestamp(),Ordering::Relaxed);
                 if tasks.len()>=16{self.health.failed.fetch_add(1,Ordering::Relaxed);continue;}
                 let request=value["d"].clone();let api=self.api.clone();let handler=self.handler.clone();let health=self.health.clone();
                 tasks.spawn(async move{let result=tokio::time::timeout(Duration::from_millis(2800),std::panic::AssertUnwindSafe(async{let response=handler(request.clone()).await;api.callback(&request,&response).await}).catch_unwind()).await;if matches!(result,Ok(Ok(Ok(())))){health.responded.fetch_add(1,Ordering::Relaxed);}else{health.failed.fetch_add(1,Ordering::Relaxed);tracing::warn!("Discord interaction response failed");}});
                },_=>{}
               },_=>{}
              }
             }
            }
        }
    }
}
enum ConnectionError {
    Retry,
    Fatal(&'static str),
}
impl Discord {
    /// Read-only startup/cutover validation. No probes, registration or messages.
    pub async fn check_channels(&self, subscriptions: &[(String, String)]) -> Result<()> {
        self.check_application().await?;
        let user = self.request(Method::GET, "/users/@me", None, false).await?;
        let id = user["id"]
            .as_str()
            .ok_or_else(|| anyhow::anyhow!("invalid Discord bot identity"))?;
        ensure!(
            user["bot"] == true && id == self.application_id.as_ref() && valid_id(id),
            "Discord bot identity does not match configured application"
        );
        let mut checked = std::collections::BTreeMap::new();
        let mut guilds = std::collections::BTreeMap::new();
        for (guild, channel) in subscriptions {
            ensure!(
                valid_id(guild) && valid_id(channel),
                "invalid subscribed Discord destination"
            );
            if let Some(previous) = checked.insert(channel, guild) {
                ensure!(previous == guild, "channel assigned to conflicting guilds");
                continue;
            }
            let metadata = self
                .request(Method::GET, &format!("/channels/{channel}"), None, false)
                .await?;
            ensure!(
                metadata["id"].as_str() == Some(channel)
                    && metadata["guild_id"].as_str() == Some(guild),
                "channel {channel} does not belong to subscribed guild {guild}"
            );
            ensure!(
                matches!(metadata["type"].as_u64(), Some(0 | 5)),
                "channel must be a text or announcement channel"
            );
            if !guilds.contains_key(guild) {
                let info = self
                    .request(Method::GET, &format!("/guilds/{guild}"), None, false)
                    .await?;
                ensure!(info["id"].as_str() == Some(guild), "invalid guild metadata");
                let member = self
                    .request(
                        Method::GET,
                        &format!("/guilds/{guild}/members/{id}"),
                        None,
                        false,
                    )
                    .await?;
                ensure!(
                    member["user"]["id"].as_str() == Some(id),
                    "invalid bot guild membership"
                );
                if let Some(until) = member["communication_disabled_until"].as_str() {
                    let t = chrono::DateTime::parse_from_rfc3339(until)
                        .map_err(|_| anyhow::anyhow!("invalid timeout metadata"))?;
                    ensure!(t <= chrono::Utc::now(), "bot is timed out in guild {guild}");
                }
                guilds.insert(guild, (info, member));
            }
            let (info, member) = &guilds[guild];
            let permissions = effective_permissions(info, member, &metadata, id)?;
            let required = (1 << 10) | (1 << 11) | (1 << 14);
            ensure!(
                permissions & required == required,
                "bot lacks View Channel, Send Messages or Embed Links in channel {channel}"
            );
        }
        Ok(())
    }
}
fn permissions(v: &Value) -> Result<u64> {
    v.as_str()
        .and_then(|s| s.parse().ok())
        .ok_or_else(|| anyhow::anyhow!("invalid Discord permission bitfield"))
}
/// Discord's everyone -> aggregate roles -> member overwrite ordering.
pub fn effective_permissions(
    guild: &Value,
    member: &Value,
    channel: &Value,
    user: &str,
) -> Result<u64> {
    let id = guild["id"]
        .as_str()
        .ok_or_else(|| anyhow::anyhow!("invalid guild ID"))?;
    if guild["owner_id"].as_str() == Some(user) {
        return Ok(u64::MAX);
    }
    let roles = member["roles"]
        .as_array()
        .ok_or_else(|| anyhow::anyhow!("invalid guild membership roles"))?;
    let guild_roles = guild["roles"]
        .as_array()
        .ok_or_else(|| anyhow::anyhow!("invalid guild roles"))?;
    let mut base = 0;
    let mut everyone = false;
    for role in guild_roles {
        if role["id"].as_str() == Some(id) {
            base |= permissions(&role["permissions"])?;
            everyone = true;
        } else if roles.iter().any(|r| r == &role["id"]) {
            base |= permissions(&role["permissions"])?;
        }
    }
    ensure!(everyone, "missing everyone role");
    if base & 8 != 0 {
        return Ok(u64::MAX);
    }
    let empty = Vec::new();
    let overwrites = channel["permission_overwrites"]
        .as_array()
        .unwrap_or(&empty);
    for ow in overwrites {
        if ow["type"].as_u64() == Some(0) && ow["id"].as_str() == Some(id) {
            base = (base & !permissions(&ow["deny"])?) | permissions(&ow["allow"])?;
        }
    }
    let (mut allow, mut deny) = (0, 0);
    for ow in overwrites {
        if ow["type"].as_u64() == Some(0) && roles.iter().any(|r| r == &ow["id"]) {
            allow |= permissions(&ow["allow"])?;
            deny |= permissions(&ow["deny"])?;
        }
    }
    base = (base & !deny) | allow;
    for ow in overwrites {
        if ow["type"].as_u64() == Some(1) && ow["id"].as_str() == Some(user) {
            base = (base & !permissions(&ow["deny"])?) | permissions(&ow["allow"])?;
        }
    }
    Ok(base)
}

fn valid_interaction(value: &Value) -> bool {
    value["id"].as_str().is_some_and(valid_id)
        && value["token"].as_str().is_some_and(|s| {
            !s.is_empty()
                && s.bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-' || b == b'.')
        })
}
