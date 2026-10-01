use anyhow::Result;
use rfd_bot::{migration::Migration, storage::Store};
#[test]
fn explicit_application_import_preserves_receipt_ownership() -> Result<()> {
    let raw = serde_json::json!({"version":1,"source_application_id":"1001","exported_at":"2026-09-29T12:00:00Z","subscriptions":[{"GuildID":"1","ChannelID":"2","DealType":"rfd_all"}],"deals":[{"DocumentID":"deal","Title":"Fixture","PostURL":"https://forums.redflagdeals.com/test","PublishedTimestamp":"2026-09-29T12:00:00Z","DiscordMessageIDs":{"2":"3"}}]});
    let m = Migration::decode(&serde_json::to_vec(&raw)?)?;
    let dir = tempfile::tempdir()?;
    let mut store = Store::open(&dir.path().join("rfd.sqlite"))?;
    assert!(store.import(&m, "1000", "1002").is_err());
    assert!(store.deal("deal")?.is_none());
    let result = store.import(&m, "1001", "1002")?;
    assert_eq!(result.message_receipts, 1);
    let d = store.deal("deal")?.unwrap();
    assert_eq!(d.discord_message_ids["2"], "3");
    assert_eq!(d.discord_message_application_ids["2"], "1001");
    store.bind_application("1002")?;
    assert!(store.import(&m, "1001", "1002").is_err());
    let mut bad = raw;
    bad["deals"][0]["unknown"] = serde_json::json!(true);
    assert!(Migration::decode(&serde_json::to_vec(&bad)?).is_err());
    Ok(())
}
