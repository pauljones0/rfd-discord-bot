use crate::models::{DealInfo, Subscription};
use std::sync::LazyLock;
const NEGATIVE: &[&str] = &[
    "above market",
    "above msrp",
    "full price",
    "inflated price",
    "negative discount",
    "no discount",
    "not a deal",
    "not warm",
    "over market",
    "over msrp",
    "overpriced",
    "price goug",
    "price hike",
    "price increase",
    "rip off",
    "ripoff",
    "scalped",
    "scalper",
];
const POSITIVE: &[&str] = &[
    "% off",
    "after cashback",
    "after coupon",
    "after rebate",
    "all time low",
    "atl",
    "cashback",
    "clearance",
    "coupon",
    "discount",
    "lowest price",
    "markdown",
    "price drop",
    "price error",
    "promo code",
    "rebate",
    "rollback",
    "sale price",
    "save ",
    "savings",
];
static CURRENCY: LazyLock<regex::Regex> = LazyLock::new(|| {
    regex::Regex::new(r"(?i)(?:ca\$|c\$|us\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)").unwrap()
});
static AMOUNT: LazyLock<regex::Regex> =
    LazyLock::new(|| regex::Regex::new(r"([0-9][0-9,]*(?:\.[0-9]{1,2})?)").unwrap());
static PERCENT: LazyLock<regex::Regex> =
    LazyLock::new(|| regex::Regex::new(r"([0-9]+(?:\.[0-9]+)?)\s*%").unwrap());
static DISCOUNT: LazyLock<regex::Regex> = LazyLock::new(|| {
    regex::Regex::new(r"(?i)(?:[0-9]+(?:\.[0-9]+)?\s*%\s*(?:off|cashback|coupon|discount|rebate)\b|\b(?:save|savings|discount|cashback|coupon|rebate)\s+[0-9]+(?:\.[0-9]+)?\s*%)").unwrap()
});
#[derive(Debug, Default, PartialEq, serde::Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct Discount {
    pub eligible: bool,
    pub reason: String,
    pub current_price: f64,
    pub original_price: f64,
    pub savings_amount: f64,
    pub discount_percent: f64,
}
fn number(raw: &str) -> Option<f64> {
    raw.trim()
        .replace(',', "")
        .parse::<f64>()
        .ok()
        .filter(|v| v.is_finite() && *v >= 0.0)
}
fn amount(raw: &str) -> Option<f64> {
    for re in [&*CURRENCY, &*AMOUNT] {
        for m in re.captures_iter(raw) {
            if let Some(v) = number(&m[1]) {
                return Some(v);
            }
        }
    }
    None
}
fn percent(raw: &str) -> Option<f64> {
    PERCENT.captures(raw).and_then(|m| number(&m[1]))
}
pub fn discount(d: &DealInfo) -> Discount {
    let text = format!("{} {} {} {}", d.title, d.summary, d.description, d.savings).to_lowercase();
    for p in NEGATIVE {
        if text.contains(p) {
            return Discount {
                reason: format!("negative value language: {p}"),
                ..Default::default()
            };
        }
    }
    if let (Some(current), Some(original)) = (amount(&d.price), amount(&d.original_price)) {
        if current >= original {
            return Discount {
                reason: "current price is not below original price".into(),
                current_price: current,
                original_price: original,
                ..Default::default()
            };
        }
        let savings = original - current;
        let pct = savings / original * 100.0;
        if pct >= 5.0 || savings >= 5.0 {
            return Discount {
                eligible: true,
                reason: "current price is below original price".into(),
                current_price: current,
                original_price: original,
                savings_amount: savings,
                discount_percent: pct,
            };
        }
    }
    if let Some(pct) = percent(&d.savings).filter(|v| *v >= 5.0) {
        return Discount {
            eligible: true,
            reason: "savings field has discount percent".into(),
            discount_percent: pct,
            ..Default::default()
        };
    }
    if let Some(amount) = amount(&d.savings).filter(|v| *v >= 5.0) {
        return Discount {
            eligible: true,
            reason: "savings field has discount amount".into(),
            savings_amount: amount,
            ..Default::default()
        };
    }
    if let Some(m) = DISCOUNT.find(&format!("{} {}", d.title, d.summary))
        && let Some(pct) = percent(m.as_str()).filter(|v| *v >= 5.0)
    {
        return Discount {
            eligible: true,
            reason: "title or summary has discount percent".into(),
            discount_percent: pct,
            ..Default::default()
        };
    }
    for p in POSITIVE {
        if text.contains(p) {
            return Discount {
                eligible: true,
                reason: format!("discount language: {p}"),
                ..Default::default()
            };
        }
    }
    Discount {
        reason: "no discount evidence".into(),
        ..Default::default()
    }
}
pub fn engaged(d: &DealInfo, ratio: f64, without_views: i64) -> bool {
    let Some(t) = d.threads.first() else {
        return false;
    };
    if t.like_count < 2 {
        return false;
    }
    let total = t.like_count.max(0) as f64 + 2.0 * t.comment_count.max(0) as f64;
    if t.view_count_available {
        t.view_count != 0 && total / t.view_count as f64 > ratio
    } else {
        total >= without_views as f64
    }
}
pub fn apply(d: &mut DealInfo) {
    if !discount(d).eligible {
        d.has_been_warm = false;
        d.has_been_hot = false;
        return;
    }
    d.has_been_warm |= engaged(d, 0.05, 15);
    d.has_been_hot |= engaged(d, 0.20, 40);
}
pub fn tech(category: &str) -> bool {
    matches!(
        category.trim().to_lowercase().as_str(),
        "computers & electronics"
            | "cameras"
            | "cell phones"
            | "cell phones & plans"
            | "computers & tablets/ereaders"
            | "home theatre & audio"
            | "peripherals & accessories"
            | "telecom"
            | "televisions"
            | "video games"
            | "pc & video games"
            | "equipment"
    )
}
pub fn eligible(d: &DealInfo, s: &Subscription) -> bool {
    let tech = tech(&d.category);
    let discount = discount(d).eligible;
    let warm = discount && (d.has_been_warm || engaged(d, 0.05, 15));
    let hot = discount && (d.has_been_hot || engaged(d, 0.20, 40));
    match s.deal_type.as_str() {
        "rfd_all" => true,
        "rfd_tech" => tech,
        "rfd_warm_hot" => warm || hot,
        "rfd_warm_hot_tech" => (warm || hot) && tech,
        "rfd_hot" => hot,
        "rfd_hot_tech" => hot && tech,
        _ => false,
    }
}
