//! Fail closed to ordinary titles if the dedicated Gemini project is billable,
//! the key belongs elsewhere, or verification is unavailable. Never log keys.
use crate::{ai::Cleaner, config::Config, control::Control, db::Db, env, http};
use anyhow::{Result, ensure};
use serde_json::Value;
use std::time::Duration;

fn unbilled(billing: &Value, project: &str) -> bool {
    billing["projectId"].as_str() == Some(project)
        && billing["billingEnabled"] == false
        && billing["billingAccountName"].as_str() == Some("")
}
fn key_matches(lookup: &Value, number: &str) -> bool {
    let parent = format!("projects/{number}/locations/global");
    lookup["parent"].as_str() == Some(parent.as_str())
        && lookup["name"].as_str().is_some_and(|name| {
            name.strip_prefix(&format!("{parent}/keys/"))
                .is_some_and(|id| !id.is_empty() && !id.contains('/'))
        })
}
async fn json(request: reqwest::RequestBuilder) -> Result<Value> {
    let response = request
        .send()
        .await
        .map_err(|_| anyhow::anyhow!("free Gemini verification transport failed"))?;
    ensure!(
        response.status().is_success(),
        "free Gemini verification rejected"
    );
    let raw = http::body(response, 16 * 1024)
        .await
        .map_err(|_| anyhow::anyhow!("free Gemini verification response failed"))?;
    serde_json::from_slice(&raw)
        .map_err(|_| anyhow::anyhow!("invalid free Gemini verification response"))
}
async fn checked(db: &Db, config: &Config) -> Result<Cleaner> {
    let project = env::value("GEMINI_FREE_PROJECT", "");
    let number = env::value("GEMINI_FREE_PROJECT_NUMBER", "");
    ensure!(
        project.starts_with("rfd-gemini-free-")
            && project
                .bytes()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-'),
        "dedicated free Gemini project required"
    );
    ensure!(
        !number.is_empty() && number.bytes().all(|c| c.is_ascii_digit()),
        "free Gemini project number required"
    );
    ensure!(
        config.gemini_keys.len() == 1 && config.gemini_models == ["gemini-3.5-flash-lite"],
        "free Gemini requires one dedicated key and the allowed lightweight model"
    );
    let client = http::client(Duration::from_secs(3), false, Some(Vec::new()))?;
    let metadata = json(client.get("http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token").header("Metadata-Flavor", "Google")).await?;
    let token = metadata["access_token"]
        .as_str()
        .filter(|t| !t.is_empty())
        .ok_or_else(|| anyhow::anyhow!("free Gemini verification identity missing"))?;
    let lookup = json(
        client
            .get("https://apikeys.googleapis.com/v2/keys:lookupKey")
            .query(&[("keyString", &config.gemini_keys[0])])
            .bearer_auth(token)
            .header("x-goog-user-project", &project),
    )
    .await?;
    ensure!(
        key_matches(&lookup, &number),
        "Gemini key belongs to another project"
    );
    let billing = json(
        client
            .get(format!(
                "https://cloudbilling.googleapis.com/v1/projects/{project}/billingInfo"
            ))
            .bearer_auth(token)
            .header("x-goog-user-project", &project),
    )
    .await?;
    ensure!(
        unbilled(&billing, &project),
        "Gemini project has billing linked or cannot be verified unbilled"
    );
    Cleaner::free_tier(
        db.clone(),
        config.gemini_keys[0].clone(),
        config.gemini_models[0].clone(),
    )
    .await
}
pub async fn cleaner(db: &Db, config: &Config, control: &Control) -> Option<Cleaner> {
    if config.gemini_keys.is_empty() {
        return None;
    }
    // Don't spend time or network verifying a provider we cannot call anyway.
    match db.call(|s| s.quota()).await {
        Ok(Some(quota)) if !crate::ai::free_ready(&quota) => return None,
        Err(_) => return None,
        _ => {}
    }
    match control
        .with_budget(Duration::from_secs(12))
        .run(checked(db, config))
        .await
    {
        Ok(Ok(cleaner)) => Some(cleaner),
        _ => {
            tracing::warn!("free Gemini verification unavailable; using original titles");
            None
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn billing_and_key_verification_fail_closed() {
        let free = json!({"projectId":"rfd-gemini-free-fixture","billingEnabled":false,"billingAccountName":""});
        assert!(unbilled(&free, "rfd-gemini-free-fixture"));
        assert!(!unbilled(&free, "another-project"));
        assert!(!unbilled(
            &json!({"projectId":"rfd-gemini-free-fixture","billingEnabled":false}),
            "rfd-gemini-free-fixture"
        ));
        assert!(!unbilled(
            &json!({"projectId":"rfd-gemini-free-fixture","billingEnabled":true,"billingAccountName":"billingAccounts/paid"}),
            "rfd-gemini-free-fixture"
        ));
        let key = json!({"parent":"projects/123/locations/global","name":"projects/123/locations/global/keys/fixture-key"});
        assert!(key_matches(&key, "123"));
        assert!(!key_matches(&key, "456"));
        assert!(!key_matches(
            &json!({"parent":"projects/123/locations/global","name":""}),
            "123"
        ));
    }
}
