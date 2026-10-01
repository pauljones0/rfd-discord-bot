use rfd_bot::time::Timestamp;
#[test]
fn go_zero_and_null_timestamps_roundtrip_without_empty_serialization() {
    let zero: Timestamp = serde_json::from_str("null").unwrap();
    assert!(zero.is_zero());
    assert_eq!(
        serde_json::to_string(&zero).unwrap(),
        "\"0001-01-01T00:00:00Z\""
    );
    assert_eq!(zero, Timestamp("0001-01-01T00:00:00Z".into()));
    assert!(serde_json::from_str::<Timestamp>("\"invalid\"").is_err());
}
