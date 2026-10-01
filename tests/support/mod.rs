#![allow(dead_code)]
use std::sync::Arc;
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
    task::JoinHandle,
};
#[derive(Clone, Debug)]
pub struct Request {
    pub method: String,
    pub path: String,
    pub headers: String,
    pub body: Vec<u8>,
}
pub struct Server {
    pub base: String,
    task: JoinHandle<()>,
}
impl Drop for Server {
    fn drop(&mut self) {
        self.task.abort();
    }
}
pub async fn server(
    handler: impl Fn(Request) -> (u16, Vec<(String, String)>, Vec<u8>) + Send + Sync + 'static,
) -> Server {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    let h = Arc::new(handler);
    let task = tokio::spawn(async move {
        loop {
            let (mut s, _) = listener.accept().await.unwrap();
            let h = h.clone();
            tokio::spawn(async move {
                let mut bytes = Vec::new();
                let mut buf = [0u8; 4096];
                let end = loop {
                    let n = s.read(&mut buf).await.unwrap();
                    assert!(n > 0);
                    bytes.extend_from_slice(&buf[..n]);
                    assert!(bytes.len() < 2 * 1024 * 1024);
                    if let Some(n) = bytes.windows(4).position(|w| w == b"\r\n\r\n") {
                        break n + 4;
                    }
                };
                let headers = String::from_utf8(bytes[..end].to_vec()).unwrap();
                let line = headers.lines().next().unwrap();
                let mut words = line.split_whitespace();
                let method = words.next().unwrap().into();
                let path = words.next().unwrap().into();
                let size = headers
                    .lines()
                    .filter_map(|l| l.split_once(':'))
                    .find(|(k, _)| k.eq_ignore_ascii_case("content-length"))
                    .map(|(_, v)| v.trim().parse::<usize>().unwrap())
                    .unwrap_or(0);
                while bytes.len() < end + size {
                    let n = s.read(&mut buf).await.unwrap();
                    assert!(n > 0);
                    bytes.extend_from_slice(&buf[..n]);
                }
                let (status, extra, body) = h(Request {
                    method,
                    path,
                    headers,
                    body: bytes[end..end + size].to_vec(),
                });
                let mut reply = format!(
                    "HTTP/1.1 {status} Fixture\r\nContent-Length: {}\r\nConnection: close\r\n",
                    body.len()
                );
                for (k, v) in extra {
                    reply.push_str(&format!("{k}: {v}\r\n"));
                }
                reply.push_str("\r\n");
                s.write_all(reply.as_bytes()).await.unwrap();
                s.write_all(&body).await.unwrap();
            });
        }
    });
    Server { base, task }
}
pub fn json(value: serde_json::Value) -> (u16, Vec<(String, String)>, Vec<u8>) {
    (
        200,
        vec![("Content-Type".into(), "application/json".into())],
        serde_json::to_vec(&value).unwrap(),
    )
}
