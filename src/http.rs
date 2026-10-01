use anyhow::{Result, ensure};
use reqwest::{Client, Response};
use std::{sync::Arc, time::Duration};
pub const MAX_BODY: usize = 5 * 1024 * 1024;
pub const USER_AGENT: &str = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36";
pub fn client(
    timeout: Duration,
    cookies: bool,
    allowed_hosts: Option<Vec<String>>,
) -> Result<Client> {
    client_with_jar(timeout, cookies, allowed_hosts, None)
}
pub fn client_with_jar(
    timeout: Duration,
    cookies: bool,
    allowed_hosts: Option<Vec<String>>,
    jar: Option<Arc<reqwest::cookie::Jar>>,
) -> Result<Client> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let roots = rustls::RootCertStore::from_iter(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
    let tls = rustls::ClientConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()?
        .with_root_certificates(roots)
        .with_no_client_auth();
    let policy = reqwest::redirect::Policy::custom(move |attempt| {
        if attempt.previous().len() >= 10 {
            return attempt.error("too many redirects");
        }
        if !matches!(attempt.url().scheme(), "https" | "http")
            || !attempt.url().username().is_empty()
            || attempt.url().password().is_some()
        {
            return attempt.error("invalid redirect URL");
        }
        if let Some(hosts) = &allowed_hosts
            && !hosts
                .iter()
                .any(|h| Some(h.as_str()) == attempt.url().host_str())
        {
            return attempt.error("redirect host is not allowed");
        }
        attempt.follow()
    });
    let builder = Client::builder()
        .use_preconfigured_tls(tls)
        .user_agent(USER_AGENT)
        .timeout(timeout)
        .connect_timeout(Duration::from_secs(10))
        .pool_idle_timeout(Duration::from_secs(60))
        .pool_max_idle_per_host(2)
        .cookie_store(cookies)
        .redirect(policy);
    let builder = if let Some(jar) = jar {
        builder.cookie_provider(jar)
    } else {
        builder
    };
    Ok(builder.build()?)
}
pub async fn body(mut response: Response, limit: usize) -> Result<Vec<u8>> {
    if let Some(size) = response.content_length() {
        ensure!(size <= limit as u64, "response body exceeds {limit} bytes");
    }
    let mut bytes =
        Vec::with_capacity(response.content_length().unwrap_or(0).min(limit as u64) as usize);
    while let Some(chunk) = response.chunk().await? {
        ensure!(
            bytes.len() + chunk.len() <= limit,
            "response body exceeds {limit} bytes"
        );
        bytes.extend_from_slice(&chunk);
    }
    Ok(bytes)
}
pub fn retry_after(headers: &reqwest::header::HeaderMap) -> Duration {
    let Some(raw) = headers
        .get(reqwest::header::RETRY_AFTER)
        .and_then(|v| v.to_str().ok())
    else {
        return Duration::ZERO;
    };
    if let Ok(seconds) = raw.parse::<f64>()
        && seconds.is_finite()
        && seconds >= 0.0
    {
        return Duration::from_secs_f64(seconds.min(120.0));
    }
    httpdate::parse_http_date(raw)
        .ok()
        .and_then(|t| t.duration_since(std::time::SystemTime::now()).ok())
        .unwrap_or_default()
        .min(Duration::from_secs(120))
}
