use rfd_bot::{models::DealInfo, notifier};
use serde_json::Value;
#[test]
fn payload_and_nonce_match_go_render_corpus() {
    let cases: Vec<Value> =
        serde_json::from_str(include_str!("fixtures/go-rfd-render.json")).unwrap();
    for (index, case) in cases.iter().enumerate() {
        let d: DealInfo = serde_json::from_value(case["deal"].clone()).unwrap();
        assert_eq!(notifier::payload(&d), case["payload"], "case {index}");
        assert_eq!(notifier::delivery(&d, "1001", "42")["nonce"], case["nonce"]);
    }
}
