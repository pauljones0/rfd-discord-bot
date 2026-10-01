mod support;
use futures_util::{FutureExt, SinkExt, StreamExt};
use rfd_bot::discord::{
    Discord, Gateway, InteractionHandler, effective_permissions, private_reply,
};
use serde_json::{Value, json};
use std::{
    sync::{
        Arc, Mutex,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};
#[tokio::test(flavor = "current_thread")]
async fn rest_rate_limit_receipt_and_credential_redirect_guard() {
    let count = Arc::new(AtomicUsize::new(0));
    let n = count.clone();
    let server = support::server(move |req| {
        assert!(req.headers.contains("Bot fixture-only"));
        assert!(
            req.headers
                .to_ascii_lowercase()
                .contains("user-agent: discordbot (")
        );
        assert_eq!(req.method, "POST");
        let v: Value = serde_json::from_slice(&req.body).unwrap();
        assert_eq!(v["enforce_nonce"], true);
        if n.fetch_add(1, Ordering::SeqCst) == 0 {
            (
                429,
                vec![],
                br#"{"retry_after":0.01,"global":true}"#.to_vec(),
            )
        } else {
            support::json(json!({"id":"123","channel_id":"456"}))
        }
    })
    .await;
    let api = Discord::with_base("fixture-only".into(), "123".into(), server.base.clone()).unwrap();
    assert_eq!(
        api.send(
            "456",
            &json!({"nonce":"same-on-retry","enforce_nonce":true})
        )
        .await
        .unwrap(),
        "123"
    );
    assert_eq!(count.load(Ordering::SeqCst), 2);
    let redirect = support::server(move |_| {
        (
            302,
            vec![(
                "Location".into(),
                "https://example.invalid/never-follow".into(),
            )],
            vec![],
        )
    })
    .await;
    let api =
        Discord::with_base("fixture-only".into(), "123".into(), redirect.base.clone()).unwrap();
    let error = api
        .request(reqwest::Method::GET, "/redirect", None, false)
        .await
        .unwrap_err();
    assert!(!error.to_string().contains("fixture-only"));
}
#[test]
fn permission_overwrites_follow_discord_order() {
    let roles = json!([{"id":"1","permissions":"3072"},{"id":"2","permissions":"16384"}]);
    let member = json!({"roles":["2"]});
    let channel = json!({"permission_overwrites":[{"id":"1","type":0,"deny":"2048","allow":"0"},{"id":"2","type":0,"deny":"0","allow":"2048"},{"id":"3","type":1,"deny":"1024","allow":"0"}]});
    let bits = effective_permissions(
        &json!({"id":"1","owner_id":"9","roles":roles}),
        &member,
        &channel,
        "3",
    )
    .unwrap();
    assert_eq!(bits & 1024, 0);
    assert_ne!(bits & 2048, 0);
    assert_ne!(bits & 16384, 0);
}
#[tokio::test(flavor = "current_thread")]
async fn gateway_identifies_without_cache_acknowledges_and_resumes() {
    let ws = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("ws://{}", ws.local_addr().unwrap());
    let gateway_url = url.clone();
    let callbacks = Arc::new(Mutex::new(Vec::new()));
    let seen = callbacks.clone();
    let server = support::server(move |req| {
        if req.path == "/gateway/bot" {
            support::json(json!({"url":gateway_url,"session_start_limit":{"remaining":1}}))
        } else {
            seen.lock()
                .unwrap()
                .push(serde_json::from_slice::<Value>(&req.body).unwrap());
            (204, vec![], vec![])
        }
    })
    .await;
    let api = Discord::with_base("fixture-only".into(), "123".into(), server.base.clone()).unwrap();
    let handler: InteractionHandler =
        Arc::new(|_| async { private_reply("fixture response") }.boxed());
    let gateway = Arc::new(Gateway::new(api, handler));
    let health = gateway.health.clone();
    let (stop, rx) = tokio::sync::watch::channel(false);
    let run = tokio::spawn(async move { gateway.run(rx).await });
    let script = tokio::spawn(async move {
        for connection in 0..2 {
            let (s, _) = ws.accept().await.unwrap();
            let mut socket = tokio_tungstenite::accept_async(s).await.unwrap();
            socket
                .send(tokio_tungstenite::tungstenite::Message::Text(
                    json!({"op":10,"d":{"heartbeat_interval":250}})
                        .to_string()
                        .into(),
                ))
                .await
                .unwrap();
            let auth: Value =
                serde_json::from_slice(&socket.next().await.unwrap().unwrap().into_data()).unwrap();
            if connection == 0 {
                assert_eq!(auth["op"], 2);
                assert_eq!(auth["d"]["intents"], 0);
            } else {
                assert_eq!(auth["op"], 6);
                assert_eq!(auth["d"]["session_id"], "fixture-session");
                assert_eq!(auth["d"]["seq"], 2);
            }
            let ready = if connection == 0 {
                json!({"op":0,"t":"READY","s":1,"d":{"session_id":"fixture-session","resume_gateway_url":url}})
            } else {
                json!({"op":0,"t":"RESUMED","s":3,"d":{}})
            };
            socket
                .send(tokio_tungstenite::tungstenite::Message::Text(
                    ready.to_string().into(),
                ))
                .await
                .unwrap();
            if connection == 0 {
                socket.send(tokio_tungstenite::tungstenite::Message::Text(json!({"op":0,"t":"INTERACTION_CREATE","s":2,"d":{"id":"777","token":"fixture-token","type":2}}).to_string().into())).await.unwrap();
            }
            while let Some(message) = socket.next().await {
                let Ok(message) = message else {
                    break;
                };
                let Ok(v) = serde_json::from_slice::<Value>(&message.into_data()) else {
                    break;
                };
                if v["op"] == 1 {
                    socket
                        .send(tokio_tungstenite::tungstenite::Message::Text(
                            json!({"op":11,"d":null}).to_string().into(),
                        ))
                        .await
                        .unwrap();
                    if connection == 0 {
                        socket
                            .send(tokio_tungstenite::tungstenite::Message::Text(
                                json!({"op":7,"d":null}).to_string().into(),
                            ))
                            .await
                            .unwrap();
                        break;
                    } else {
                        stop.send(true).unwrap();
                        break;
                    }
                }
            }
        }
    });
    tokio::time::timeout(Duration::from_secs(12), script)
        .await
        .unwrap()
        .unwrap();
    tokio::time::timeout(Duration::from_secs(4), run)
        .await
        .unwrap()
        .unwrap()
        .unwrap();
    assert_eq!(health.responded.load(Ordering::Relaxed), 1);
    assert_eq!(callbacks.lock().unwrap()[0]["data"]["flags"], 64);
}
