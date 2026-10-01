use std::sync::LazyLock;
use url::Url;
static ASIN: LazyLock<regex::Regex> = LazyLock::new(|| {
    regex::Regex::new(r"(?:/dp/|/gp/product/)([\w0-9]+)|/[^/]+/dp/([\w0-9]+)").unwrap()
});
static EBAY_ITEM: LazyLock<regex::Regex> =
    LazyLock::new(|| regex::Regex::new(r"/itm/(?:[^/]+/)?(\d{10,13})").unwrap());
static EBAY_PRODUCT: LazyLock<regex::Regex> =
    LazyLock::new(|| regex::Regex::new(r"/p/(\d+)").unwrap());
fn host_matches(host: &str, domains: &[&str]) -> bool {
    domains
        .iter()
        .any(|d| host == *d || host.ends_with(&format!(".{d}")))
}
fn get(u: &Url, key: &str) -> String {
    u.query_pairs()
        .find(|(k, _)| k == key)
        .map(|(_, v)| v.into_owned())
        .unwrap_or_default()
}
fn query(u: &mut Url, pairs: Vec<(String, String)>) {
    u.set_query(None);
    if !pairs.is_empty() {
        let mut pairs = pairs;
        pairs.sort_by(|a, b| a.0.cmp(&b.0));
        u.query_pairs_mut().extend_pairs(pairs);
    }
}
fn remove_tracking(u: &mut Url, keys: &[&str]) {
    let q = u
        .query_pairs()
        .filter(|(k, _)| !k.to_lowercase().starts_with("utm_") && !keys.contains(&k.as_ref()))
        .map(|(k, v)| (k.into_owned(), v.into_owned()))
        .collect();
    query(u, q);
}
fn item_id(u: &Url) -> String {
    if let Some(m) = EBAY_ITEM.captures(u.path()) {
        return m[1].into();
    }
    let iid = get(u, "iid");
    if (10..=13).contains(&iid.len()) && iid.bytes().all(|b| b.is_ascii_digit()) {
        iid
    } else {
        String::new()
    }
}
pub fn clean_product(raw: &str) -> String {
    let Ok(mut u) = Url::parse(raw) else {
        return raw.into();
    };
    let host = u.host_str().unwrap_or("").to_lowercase();
    if host_matches(&host, &["amazon.ca", "amazon.com"]) {
        let asin = ASIN
            .captures(u.path())
            .and_then(|m| m.get(1).or_else(|| m.get(2)))
            .map(|m| m.as_str().to_string());
        if let Some(asin) = asin {
            u.set_path(&format!("/dp/{asin}"));
            let q = ["th", "psc", "smid"]
                .into_iter()
                .filter_map(|k| {
                    let v = get(&u, k);
                    if v.is_empty() {
                        None
                    } else {
                        Some((k.into(), v))
                    }
                })
                .collect();
            query(&mut u, q);
        } else {
            remove_tracking(
                &mut u,
                &[
                    "tag",
                    "ref",
                    "ref_",
                    "linkCode",
                    "camp",
                    "creative",
                    "ascsubtag",
                ],
            );
        }
        return u.to_string();
    }
    if host_matches(&host, &["ebay.ca", "ebay.com"]) {
        let id = item_id(&u);
        let product = EBAY_PRODUCT.captures(u.path()).map(|m| m[1].to_string());
        if !id.is_empty() {
            u.set_path(&format!("/itm/{id}"));
            u.set_query(None);
        } else if let Some(id) = product {
            u.set_path(&format!("/p/{id}"));
            u.set_query(None);
        } else {
            remove_tracking(
                &mut u,
                &[
                    "_trkparms",
                    "_trksid",
                    "mkcid",
                    "mkrid",
                    "campid",
                    "toolid",
                    "customid",
                    "mkevt",
                ],
            );
        }
        return u.to_string();
    }
    if host_matches(&host, &["bestbuy.ca", "bestbuy.com"]) {
        if u.path().contains("/product/")
            || (u.path().starts_with("/site/") && u.path().ends_with(".p"))
        {
            u.set_query(None);
        } else {
            remove_tracking(
                &mut u,
                &["cmp", "cmpid", "irclickid", "irgwc", "loc", "ref"],
            );
        }
        return u.to_string();
    }
    raw.into()
}
fn escape_query(raw: &str) -> String {
    url::form_urlencoded::Serializer::new(String::new())
        .append_pair("", raw)
        .finish()[1..]
        .into()
}
pub fn referral(raw: &str, amazon_tag: &str, bestbuy_prefix: &str) -> String {
    referral_depth(raw, amazon_tag, bestbuy_prefix, 0)
}
fn referral_depth(raw: &str, amazon_tag: &str, bestbuy_prefix: &str, depth: usize) -> String {
    if depth >= 8 {
        return raw.into();
    }
    let Ok(mut u) = Url::parse(raw) else {
        return raw.into();
    };
    let host = u.host_str().unwrap_or("");
    if host == "click.linksynergy.com" || host == "go.redirectingat.com" {
        let target = get(
            &u,
            if host == "click.linksynergy.com" {
                "murl"
            } else {
                "url"
            },
        );
        if target.starts_with("http://") || target.starts_with("https://") {
            return referral_depth(&target, amazon_tag, bestbuy_prefix, depth + 1);
        }
        return raw.into();
    }
    if host == "bestbuyca.o93x.net" && u.path().starts_with("/c/") {
        let target = get(&u, "u");
        if target.is_empty() {
            return raw.into();
        }
        return if bestbuy_prefix.is_empty() {
            target
        } else {
            format!("{bestbuy_prefix}{}", escape_query(&target))
        };
    }
    if host.ends_with("bestbuy.ca") && !bestbuy_prefix.is_empty() {
        return format!("{bestbuy_prefix}{}", escape_query(raw));
    }
    if host.contains("amazon.") {
        let original = get(&u, "tag");
        if original == amazon_tag
            && (!amazon_tag.is_empty() || !u.query_pairs().any(|(k, _)| k == "tag"))
        {
            return raw.into();
        }
        let mut q: Vec<_> = u
            .query_pairs()
            .filter(|(k, _)| k != "tag")
            .map(|(k, v)| (k.into_owned(), v.into_owned()))
            .collect();
        if !amazon_tag.is_empty() {
            q.push(("tag".into(), amazon_tag.into()));
        }
        query(&mut u, q);
        return u.to_string();
    }
    if host_matches(host, &["ebay.ca", "ebay.com"]) {
        let id = item_id(&u);
        if !id.is_empty() {
            let host = if host_matches(host, &["ebay.ca"]) {
                "www.ebay.ca"
            } else {
                "www.ebay.com"
            };
            return format!("https://{host}/itm/{id}");
        }
    }
    raw.into()
}
pub fn unwrap_referral(raw: &str) -> String {
    let mut raw = raw.trim().to_string();
    for _ in 0..3 {
        let Ok(u) = Url::parse(&raw) else {
            return raw;
        };
        let host = u.host_str().unwrap_or("");
        let key = match host {
            "click.linksynergy.com" => "murl",
            "go.redirectingat.com" => "url",
            "bestbuyca.o93x.net" if u.path().starts_with("/c/") => "u",
            _ => return raw,
        };
        let target = get(&u, key);
        let target = target.trim();
        if target.starts_with("https://") || target.starts_with("http://") {
            raw = target.into();
        } else {
            return raw;
        }
    }
    raw
}
pub fn canonical(raw: &str) -> String {
    if raw.trim().is_empty() {
        return String::new();
    }
    let cleaned = clean_product(&unwrap_referral(raw));
    let Ok(mut u) = Url::parse(&cleaned) else {
        return cleaned.trim_end_matches('/').into();
    };
    u.set_fragment(None);
    let host = u.host_str().unwrap_or("").to_lowercase();
    let host = host.trim_start_matches("www.");
    let _ = u.set_host(Some(host));
    if u.path() != "/" {
        let path = u.path().trim_end_matches('/').to_string();
        u.set_path(&path);
    }
    if host_matches(host, &["amazon.ca", "amazon.com"])
        && u.path().starts_with("/dp/")
        && !u.path()[4..].contains('/')
    {
        u.set_query(None);
    }
    u.to_string()
}
pub fn same(left: &str, right: &str) -> bool {
    let left = canonical(left);
    !left.is_empty() && left == canonical(right)
}
pub fn thread_key(raw: &str) -> String {
    let raw = raw.split('#').next().unwrap_or("").trim_end_matches('/');
    if let Ok(u) = Url::parse(raw)
        && u.host_str()
            .unwrap_or("")
            .to_lowercase()
            .contains("redflagdeals.com")
    {
        let path = u.path().trim_end_matches('/');
        let slug = path.rsplit('/').next().unwrap_or("");
        if let Some((_, id)) = slug.rsplit_once('-')
            && !id.is_empty()
            && id.bytes().all(|b| b.is_ascii_digit())
        {
            return format!("rfd:{id}");
        }
    }
    raw.into()
}
