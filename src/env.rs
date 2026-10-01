use anyhow::{Result, bail, ensure};
use std::time::Duration;
pub fn value(key: &str, fallback: &str) -> String {
    std::env::var(key)
        .ok()
        .map(|v| v.trim().to_string())
        .filter(|v| !v.is_empty())
        .unwrap_or_else(|| fallback.into())
}
pub fn csv(raw: &str) -> Vec<String> {
    raw.split(',')
        .map(str::trim)
        .filter(|v| !v.is_empty())
        .map(str::to_string)
        .collect()
}
pub fn boolean(key: &str, fallback: bool) -> Result<bool> {
    let raw = value(key, if fallback { "true" } else { "false" });
    match raw.to_lowercase().as_str() {
        "1" | "true" | "t" => Ok(true),
        "0" | "false" | "f" => Ok(false),
        _ => bail!("{key} must be a boolean"),
    }
}
pub fn parse_duration(raw: &str, zero: bool) -> Result<Duration> {
    if raw == "0" {
        ensure!(zero, "duration must be positive");
        return Ok(Duration::ZERO);
    }
    let re = regex::Regex::new(r"([0-9]+(?:\.[0-9]+)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h)")?;
    let mut pos = 0;
    let mut seconds = 0.0;
    for m in re.captures_iter(raw) {
        let all = m.get(0).unwrap();
        ensure!(all.start() == pos, "invalid duration");
        pos = all.end();
        let n = m[1].parse::<f64>()?;
        let unit = match &m[2] {
            "ns" => 1e-9,
            "us" | "µs" | "μs" => 1e-6,
            "ms" => 1e-3,
            "s" => 1.0,
            "m" => 60.0,
            "h" => 3600.0,
            _ => unreachable!(),
        };
        seconds += n * unit;
    }
    ensure!(
        pos == raw.len()
            && pos > 0
            && seconds.is_finite()
            && seconds >= 0.0
            && (zero || seconds > 0.0)
            && seconds <= i64::MAX as f64 / 1e9,
        "invalid duration"
    );
    Ok(Duration::try_from_secs_f64(seconds)?)
}
pub fn duration(key: &str, fallback: &str, zero: bool) -> Result<Duration> {
    parse_duration(&value(key, fallback), zero).map_err(|_| {
        anyhow::anyhow!(
            "{key} must be a valid {} duration",
            if zero { "non-negative" } else { "positive" }
        )
    })
}
pub fn positive(key: &str, fallback: &str) -> Result<usize> {
    let n = value(key, fallback)
        .parse::<usize>()
        .map_err(|_| anyhow::anyhow!("{key} must be a positive integer"))?;
    ensure!(n > 0, "{key} must be a positive integer");
    Ok(n)
}
