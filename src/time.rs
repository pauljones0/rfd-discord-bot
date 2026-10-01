use chrono::{DateTime, SecondsFormat, Utc};
use serde::{Deserialize, Serialize};

/// Retains Go's RFC3339Nano representation, including its zero timestamp.
#[derive(Debug, Clone, Default)]
pub struct Timestamp(pub String);

impl Timestamp {
    pub fn as_str(&self) -> &str {
        if self.0.is_empty() {
            "0001-01-01T00:00:00Z"
        } else {
            &self.0
        }
    }
    pub fn now() -> Self {
        Self::from_datetime(Utc::now())
    }
    pub fn from_datetime(t: DateTime<Utc>) -> Self {
        {
            let raw = t.to_rfc3339_opts(SecondsFormat::Nanos, true);
            let raw = raw.trim_end_matches('Z');
            Self(format!(
                "{}Z",
                raw.trim_end_matches('0').trim_end_matches('.')
            ))
        }
    }
    pub fn from_fixed(t: DateTime<chrono::FixedOffset>) -> Self {
        let raw = t.to_rfc3339_opts(SecondsFormat::Nanos, true);
        let dot = raw.find('.').expect("nanosecond formatter");
        let end = raw[dot + 1..]
            .find(['Z', '+', '-'])
            .map(|i| dot + 1 + i)
            .unwrap_or(raw.len());
        let fraction = raw[dot + 1..end].trim_end_matches('0');
        Self(format!(
            "{}{}{}{}",
            &raw[..dot],
            if fraction.is_empty() { "" } else { "." },
            fraction,
            &raw[end..]
        ))
    }
    pub fn parse(&self) -> anyhow::Result<DateTime<Utc>> {
        Ok(DateTime::parse_from_rfc3339(self.as_str())?.with_timezone(&Utc))
    }
    pub fn nanos(&self) -> anyhow::Result<i64> {
        let t = self.parse()?;
        Ok(
            (i128::from(t.timestamp()) * 1_000_000_000 + i128::from(t.timestamp_subsec_nanos()))
                as i64,
        )
    }
    pub fn is_zero(&self) -> bool {
        self.0 == "0001-01-01T00:00:00Z" || self.0.is_empty()
    }
}
pub fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

impl<'de> Deserialize<'de> for Timestamp {
    fn deserialize<D: serde::Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        let Some(raw) = Option::<String>::deserialize(d)? else {
            return Ok(Self::default());
        };
        if raw.is_empty() || raw == "0001-01-01T00:00:00Z" {
            return Ok(Self::default());
        }
        if DateTime::parse_from_rfc3339(&raw).is_ok() {
            return Ok(Self(raw));
        }
        if let Ok(t) = DateTime::parse_from_str(&raw, "%Y-%m-%d %H:%M:%S %z %Z") {
            return Ok(Self::from_datetime(t.with_timezone(&Utc)));
        }
        Err(serde::de::Error::custom("invalid timestamp"))
    }
}

impl PartialEq for Timestamp {
    fn eq(&self, other: &Self) -> bool {
        self.as_str() == other.as_str()
    }
}
impl Eq for Timestamp {}
impl Serialize for Timestamp {
    fn serialize<S: serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(self.as_str())
    }
}
