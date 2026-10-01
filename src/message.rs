use serde::Serialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
#[derive(Default, Debug, Clone, Serialize)]
pub struct Embed {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub url: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub timestamp: String,
    pub color: u32,
    pub thumbnail: Thumbnail,
    pub footer: Footer,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub fields: Vec<Field>,
}
#[derive(Default, Debug, Clone, Serialize)]
pub struct Thumbnail {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub url: String,
}
#[derive(Default, Debug, Clone, Serialize)]
pub struct Footer {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub text: String,
}
#[derive(Debug, Clone, Serialize)]
pub struct Field {
    pub name: String,
    pub value: String,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub inline: bool,
}
pub const COLD: u32 = 2829617;
pub const WARM: u32 = 16098851;
pub const HOT: u32 = 16723320;
pub fn payload(embeds: &[Embed], nonce: Option<&str>) -> Value {
    let mut p = json!({"content":"","embeds":embeds,"allowed_mentions":{"parse":[]}});
    if let Some(n) = nonce {
        p["nonce"] = json!(n);
        p["enforce_nonce"] = json!(true);
    }
    p
}
pub fn nonce(identity: &str) -> String {
    hex::encode(&Sha256::digest(identity.as_bytes())[..12])
}
pub fn valid_url(raw: &str) -> String {
    let raw = raw.trim();
    if raw
        .chars()
        .any(|c| c.is_ascii_whitespace() || c == '<' || c == '>')
    {
        return String::new();
    }
    url::Url::parse(raw)
        .ok()
        .filter(|u| {
            matches!(u.scheme(), "http" | "https")
                && u.host_str().is_some()
                && u.username().is_empty()
                && u.password().is_none()
        })
        .map(|_| raw.to_owned())
        .unwrap_or_default()
}
pub fn units(s: &str) -> usize {
    s.encode_utf16().count()
}
pub fn limit(s: &str, max: usize) -> String {
    if units(s) <= max {
        return s.to_owned();
    }
    let mut out = String::new();
    let mut left = max.saturating_sub(1);
    for c in s.chars() {
        if c.len_utf16() > left {
            break;
        }
        out.push(c);
        left -= c.len_utf16();
    }
    if max > 0 {
        out.push('…');
    }
    out
}
impl Embed {
    pub fn units(&self) -> usize {
        units(&self.title)
            + units(&self.description)
            + units(&self.footer.text)
            + self
                .fields
                .iter()
                .map(|f| units(&f.name) + units(&f.value))
                .sum::<usize>()
    }
    pub fn bound(&mut self) {
        self.title = limit(&self.title, 256);
        self.description = limit(&self.description, 4096);
        self.footer.text = limit(&self.footer.text, 2048);
        self.fields.truncate(25);
        for f in &mut self.fields {
            f.name = limit(&f.name, 256);
            f.value = limit(&f.value, 1024);
        }
        while self.units() > 6000 {
            if self.fields.len() > 1 {
                self.fields.pop();
            } else {
                self.description = limit(
                    &self.description,
                    4096usize
                        .min(6000usize.saturating_sub(self.units() - units(&self.description))),
                );
                break;
            }
        }
    }
}
