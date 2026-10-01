use rfd_bot::{commands, db::Db, storage::Store};
use serde_json::json;
#[tokio::test(flavor = "current_thread")]
async fn commands_enforce_permissions_validate_filter_and_scope_unsubscribe_to_guild() {
    let tmp = tempfile::tempdir().unwrap();
    let (db, worker) = Db::new(Store::open(&tmp.path().join("fixture.sqlite")).unwrap()).unwrap();
    let mut req = json!({"type":2,"guild_id":"1","member":{"permissions":"0","user":{"id":"1002"}},"data":{"name":"rfd","resolved":{"channels":{"42":{"type":0,"name":"fixture"}}},"options":[{"name":"subscribe","options":[{"name":"channel","value":"42"},{"name":"filter","value":"rfd_all"}]}]}});
    assert!(commands::handle(&db, &req).await.contains("permission"));
    assert_eq!(db.call(|s| s.subscriptions(None)).await.unwrap().len(), 0);
    req["member"]["permissions"] = json!("32");
    assert!(commands::handle(&db, &req).await.contains("enabled"));
    req["guild_id"] = json!("2");
    commands::handle(&db, &req).await;
    req["guild_id"] = json!("1");
    req["data"]["options"][0]["name"] = json!("unsubscribe");
    commands::handle(&db, &req).await;
    let subs = db.call(|s| s.subscriptions(None)).await.unwrap();
    assert_eq!(subs.len(), 1);
    assert_eq!(subs[0].guild_id, "2");
    req["data"]["options"][0]["name"] = json!("subscribe");
    req["data"]["options"][0]["options"][1]["value"] = json!("invalid");
    assert!(commands::handle(&db, &req).await.contains("available"));
    req["guild_id"] = json!("");
    assert!(commands::handle(&db, &req).await.contains("server"));
    drop(db);
    worker.join().unwrap();
}
