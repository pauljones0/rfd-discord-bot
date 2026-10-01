use crate::{
    db::Db,
    discord::{self, InteractionHandler},
    models::Subscription,
    storage::valid_filter,
    time::Timestamp,
};
use futures_util::FutureExt;
use serde_json::{Value, json};
use std::{sync::Arc, time::Duration};
pub const CHOICES: &[(&str, &str)] = &[
    ("All deals", "rfd_all"),
    ("Tech only", "rfd_tech"),
    ("Warm + Hot (all)", "rfd_warm_hot"),
    ("Warm + Hot (tech)", "rfd_warm_hot_tech"),
    ("Hot only (all)", "rfd_hot"),
    ("Hot only (tech)", "rfd_hot_tech"),
];
fn label(kind: &str) -> &str {
    CHOICES
        .iter()
        .find(|(_, k)| *k == kind)
        .map(|(l, _)| *l)
        .unwrap_or(kind)
}
pub fn command() -> Value {
    let channel = json!({"type":7,"name":"channel","description":"Channel for RFD alerts","required":true,"channel_types":[0,5]});
    let filter = |required: bool| json!({"type":3,"name":"filter","description":"Which deals to include","required":required,"choices":CHOICES.iter().map(|(n,v)|json!({"name":n,"value":v})).collect::<Vec<_>>()});
    json!({"name":"rfd","description":"Manage RedFlagDeals alerts","default_member_permissions":"32","dm_permission":false,"options":[{"type":1,"name":"subscribe","description":"Enable RFD alerts in a channel","options":[channel,filter(true)]},{"type":1,"name":"unsubscribe","description":"Remove RFD alerts from a channel","options":[channel,filter(false)]},{"type":1,"name":"list","description":"List this server's RFD subscriptions"}]})
}
pub fn handler(db: Db) -> InteractionHandler {
    Arc::new(move |req| {
        let db = db.clone();
        async move { discord::private_reply(handle(&db, &req).await) }.boxed()
    })
}
fn option(options: &Value, name: &str) -> String {
    options
        .as_array()
        .and_then(|a| a.iter().find(|o| o["name"] == name))
        .and_then(|o| o["value"].as_str())
        .unwrap_or("")
        .into()
}
pub async fn handle(db: &Db, req: &Value) -> String {
    if req["type"] != 2 || req["data"]["name"] != "rfd" {
        return "Use an /rfd command.".into();
    }
    let guild = req["guild_id"].as_str().unwrap_or("");
    if guild.is_empty() || req["member"].is_null() {
        return "Use this command in a Discord server.".into();
    }
    if !discord::can_manage(req) {
        return "You need Manage Server permission to manage RFD alerts.".into();
    }
    let Some(options) = req["data"]["options"].as_array().filter(|a| a.len() == 1) else {
        return "Choose subscribe, unsubscribe, or list.".into();
    };
    let sub = &options[0];
    match sub["name"].as_str() {
        Some("list") => {
            let guild = guild.to_string();
            let result = db
                .call_until(Duration::from_millis(1500), move |s| {
                    s.subscriptions(Some(&guild))
                })
                .await;
            let Ok(subs) = result else {
                return "Could not read subscriptions. Please try again.".into();
            };
            if subs.is_empty() {
                return "No RFD subscriptions yet. Use /rfd subscribe to add one.".into();
            }
            let mut out = String::new();
            for (i, s) in subs.iter().enumerate() {
                let line = format!("<#{}> — {}\n", s.channel_id, label(&s.deal_type));
                if out.len() + line.len() > 1800 {
                    out.push_str(&format!("…and {} more subscriptions.", subs.len() - i));
                    break;
                }
                out.push_str(&line);
            }
            out
        }
        Some("subscribe" | "unsubscribe") => {
            let channel = option(&sub["options"], "channel");
            let filter = option(&sub["options"], "filter");
            let resolved = &req["data"]["resolved"]["channels"][&channel];
            if channel.is_empty() || !matches!(resolved["type"].as_u64(), Some(0 | 5)) {
                return "Select a text or announcement channel in this server.".into();
            }
            if sub["name"] == "subscribe" {
                if !valid_filter(&filter) {
                    return "Select one of the available RFD filters.".into();
                }
                let record = Subscription {
                    guild_id: guild.into(),
                    channel_id: channel.clone(),
                    channel_name: resolved["name"].as_str().unwrap_or("").into(),
                    deal_type: filter.clone(),
                    subscription_type: "rfd".into(),
                    added_by: req["member"]["user"]["id"].as_str().unwrap_or("").into(),
                    added_at: Timestamp::now(),
                };
                if db
                    .call_until(Duration::from_millis(1500), move |s| {
                        s.save_subscription(&record)
                    })
                    .await
                    .is_err()
                {
                    return "Could not save this subscription. Please try again.".into();
                }
                format!("RFD alerts enabled in <#{channel}>: {}.", label(&filter))
            } else {
                if !filter.is_empty() && !valid_filter(&filter) {
                    return "Select a valid RFD filter, or omit it to remove all RFD filters for this channel.".into();
                }
                let (guild, ch) = (guild.to_string(), channel.clone());
                let filter = if filter.is_empty() {
                    None
                } else {
                    Some(filter)
                };
                if db
                    .call_until(Duration::from_millis(1500), move |s| {
                        s.remove_subscription(&guild, &ch, filter.as_deref())
                    })
                    .await
                    .is_err()
                {
                    return "Could not remove this subscription. Please try again.".into();
                }
                format!("Removed the selected RFD subscription(s) from <#{channel}>.")
            }
        }
        _ => "Unknown RFD subcommand.".into(),
    }
}
