//! Bounded HTTP/1 server for loopback health checks and optional interactions.
use anyhow::{Result, ensure};
use bytes::Bytes;
use futures_util::future::BoxFuture;
use http_body_util::{BodyExt, Full};
use hyper::{Request, Response, body::Incoming, server::conn::http1, service::service_fn};
use hyper_util::rt::{TokioIo, TokioTimer};
use serde_json::Value;
use std::{sync::Arc, time::Duration};
use tokio::{net::TcpListener, sync::watch, task::JoinSet};
pub type Reply = Response<Full<Bytes>>;
pub type Handler = Arc<dyn Fn(Request<Incoming>) -> BoxFuture<'static, Reply> + Send + Sync>;
pub fn response(status: u16, content: &str, body: impl Into<Bytes>) -> Reply {
    Response::builder()
        .status(status)
        .header("Content-Type", content)
        .header("Cache-Control", "no-store")
        .body(Full::new(body.into()))
        .expect("static response headers")
}
pub fn json(status: u16, value: Value) -> Reply {
    response(
        status,
        "application/json",
        serde_json::to_vec(&value).expect("JSON Value serialization"),
    )
}
pub fn text(status: u16, body: impl Into<String>) -> Reply {
    response(status, "text/plain; charset=utf-8", body.into())
}
pub async fn body(mut incoming: Incoming, limit: usize) -> Result<Vec<u8>> {
    let mut out = Vec::new();
    while let Some(frame) = incoming.frame().await {
        let frame = frame?;
        if let Ok(data) = frame.into_data() {
            ensure!(
                out.len() + data.len() <= limit,
                "HTTP request body too large"
            );
            out.extend_from_slice(&data);
        }
    }
    Ok(out)
}
pub async fn serve(
    listener: TcpListener,
    handler: Handler,
    mut stop: watch::Receiver<bool>,
) -> Result<()> {
    let mut connections = JoinSet::new();
    loop {
        if *stop.borrow() {
            break;
        }
        tokio::select! {
         _=stop.changed()=>break,
         _=connections.join_next(),if !connections.is_empty()=>{},
         socket=listener.accept()=>{
          let(socket,_)=socket?;if connections.len()>=16{drop(socket);continue;}let handler=handler.clone();let mut stop=stop.clone();
          connections.spawn(async move{
           let service=service_fn(move|request|{let handler=handler.clone();async move{Ok::<_,std::convert::Infallible>(handler(request).await)}});
           let mut builder=http1::Builder::new();builder.timer(TokioTimer::new()).header_read_timeout(Duration::from_secs(5)).max_buf_size(32768).max_headers(64);
           let connection=builder.serve_connection(TokioIo::new(socket),service);tokio::pin!(connection);
           tokio::select!{_=&mut connection=>{},_=stop.changed()=>{connection.as_mut().graceful_shutdown();let _=tokio::time::timeout(Duration::from_secs(30),connection).await;}}
          });
         }
        }
    }
    let _ = tokio::time::timeout(Duration::from_secs(31), async {
        while connections.join_next().await.is_some() {}
    })
    .await;
    connections.abort_all();
    Ok(())
}
pub async fn healthcheck(listen: &str) -> Result<()> {
    let address = if let Some(port) = listen.strip_prefix("0.0.0.0:") {
        format!("127.0.0.1:{port}")
    } else if let Some(port) = listen.strip_prefix("[::]:") {
        format!("[::1]:{port}")
    } else {
        listen.into()
    };
    let http = crate::http::client(Duration::from_secs(3), false, Some(Vec::new()))?;
    let response = http.get(format!("http://{address}/health")).send().await?;
    ensure!(
        response.status().is_success(),
        "health returned {}",
        response.status()
    );
    Ok(())
}
