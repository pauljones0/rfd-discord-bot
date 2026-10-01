pub fn emoji(category: &str) -> &'static str {
    known(&normalize(category).to_lowercase()).unwrap_or("❌")
}

/// Repair labels joined by the old desktop/mobile selector. Only collapse a
/// repeated value when the result is a known category; never guess a new label.
pub fn normalize(category: &str) -> String {
    let value = category.split_whitespace().collect::<Vec<_>>().join(" ");
    let value = value.trim_start_matches("Category:").trim();
    let middle = value.len() / 2;
    if value.is_char_boundary(middle) {
        let (left, right) = value.split_at(middle);
        let left = left.trim();
        if left.eq_ignore_ascii_case(right.trim()) && known(&left.to_lowercase()).is_some() {
            return left.into();
        }
    }
    value.into()
}

fn known(category: &str) -> Option<&'static str> {
    Some(match category {
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
        | "pc & video games" => "💻",
        "apparel"
        | "baby apparel"
        | "children's apparel"
        | "men's apparel"
        | "men's clothing"
        | "men's shoes"
        | "women's apparel"
        | "women's clothing"
        | "women's shoes"
        | "clothing & accessories" => "👕",
        "automotive" | "auto parts & accessories" | "auto services" | "motor vehicles" => "🚗",
        "beauty & wellness" | "beauty supplies & personal care" | "salons & spas" => "💄",
        "entertainment" | "books, music, movies, magazines" | "events & attractions" => "🍿",
        "financial services"
        | "personal finance"
        | "banking & investing"
        | "credit cards"
        | "insurance"
        | "mortgages & loans" => "💰",
        "groceries" | "coffee & desserts" => "🛒",
        "restaurants" | "fast food" | "restaurants & bars" => "🍔",
        "home & garden"
        | "appliances"
        | "furniture"
        | "home decor"
        | "home improvement & tools"
        | "home services & repairs"
        | "outdoors & patio" => "🏡",
        "kids & babies" | "baby needs" | "toys & games" => "👶",
        "sports & fitness" | "gyms & related services" => "⚽",
        "travel" | "flights" | "hotels" | "rail & bus" | "vacations & cruises" | "car rentals" => {
            "✈️"
        }
        "small business" | "office supplies" => "💼",
        "pets" => "🐾",
        "school supplies" => "🎒",
        "shopping discussion" => "🛍️",
        "request-a-deal" => "❓",
        "careers" | "services" => "👔",
        "expired offers" => "⏳",
        "other" | "equipment" => "🏷️",
        _ => return None,
    })
}
