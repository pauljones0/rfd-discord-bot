use anyhow::Result;
use rfd_bot::{
    models::{DealInfo, Subscription, ThreadContext},
    storage::Store,
    time::Timestamp,
};
fn deal(id: &str, thread: &str) -> DealInfo {
    DealInfo {
        document_id: id.into(),
        title: "Fixture sale".into(),
        published_timestamp: Timestamp::now(),
        last_updated: Timestamp::now(),
        threads: vec![ThreadContext {
            document_id: thread.into(),
            ..Default::default()
        }],
        ..Default::default()
    }
}
#[test]
fn compatible_reopen_receipts_aliases_and_retention() -> Result<()> {
    let dir = tempfile::tempdir()?;
    let path = dir.path().join("fixture.sqlite");
    let mut store = Store::open(&path)?;
    store.bind_application("1001")?;
    let mut d = deal("canonical", "alias");
    d.discord_message_ids.insert("42".into(), "99".into());
    d.discord_message_application_ids
        .insert("42".into(), "1001".into());
    store.batch_write(&[d.clone()], &[])?;
    drop(store);
    let mut store = Store::open(&path)?;
    store.bind_application("1001")?;
    assert!(store.bind_application("1002").is_err());
    assert_eq!(store.by_ids(&["alias".into()])?["alias"], d);
    assert_eq!(
        store.deal("canonical")?.unwrap().discord_message_ids["42"],
        "99"
    );
    let mut changed = d.clone();
    changed.title = "A changed title".into();
    assert!(store.batch_write(&[d.clone()], &[changed]).is_err());
    assert_eq!(store.deal("canonical")?.unwrap().title, d.title);
    store.batch_write(
        &[deal("second", "ambiguous"), deal("third", "ambiguous")],
        &[],
    )?;
    assert!(store.by_ids(&["ambiguous".into()]).is_err());
    store.maintain(1)?;
    assert!(store.deal("canonical")?.is_none());
    assert!(!store.by_ids(&["alias".into()])?.contains_key("alias"));
    store.integrity_check()?;
    Ok(())
}
#[test]
fn reject_foreign_schema_without_mutation() -> Result<()> {
    let dir = tempfile::tempdir()?;
    let path = dir.path().join("foreign.sqlite");
    let db = rusqlite::Connection::open(&path)?;
    db.execute_batch("CREATE TABLE documents(x TEXT); INSERT INTO documents VALUES('keep');")?;
    assert!(Store::open(&path).is_err());
    assert_eq!(
        db.query_row("PRAGMA journal_mode", [], |r| r.get::<_, String>(0))?,
        "delete"
    );
    assert_eq!(
        db.query_row("SELECT x FROM documents", [], |r| r.get::<_, String>(0))?,
        "keep"
    );
    Ok(())
}
#[test]
fn scoped_subscriptions_and_corrupt_identity() -> Result<()> {
    let dir = tempfile::tempdir()?;
    let path = dir.path().join("s.sqlite");
    let store = Store::open(&path)?;
    for guild in ["1", "2"] {
        store.save_subscription(&Subscription {
            guild_id: guild.into(),
            channel_id: "10".into(),
            deal_type: "rfd_all".into(),
            ..Default::default()
        })?;
    }
    store.remove_subscription("1", "10", None)?;
    assert_eq!(store.subscriptions(None)?.len(), 1);
    let db = rusqlite::Connection::open(path)?;
    db.execute("INSERT INTO deals VALUES('bad','{}',0,0)", [])?;
    assert!(store.deal("bad").is_err());
    Ok(())
}
#[test]
fn timestamp_format_matches_go_nano() -> Result<()> {
    let t = chrono::DateTime::parse_from_rfc3339("2026-09-29T12:00:00.123400000Z")?
        .with_timezone(&chrono::Utc);
    assert_eq!(Timestamp::from_datetime(t).0, "2026-09-29T12:00:00.1234Z");
    assert_eq!(Timestamp::default().nanos()?, -6795364578871345152);
    Ok(())
}
#[test]
fn snapshot_includes_committed_wal_and_refuses_overwrite() -> anyhow::Result<()> {
    let temp = tempfile::tempdir()?;
    let path = temp.path().join("source.sqlite");
    let mut store = Store::open(&path)?;
    store.bind_application("1001")?;
    let backup = temp.path().join("backup.sqlite");
    let args = vec![
        "--database".into(),
        path.to_string_lossy().into_owned(),
        "--destination".into(),
        backup.to_string_lossy().into_owned(),
    ];
    rfd_bot::backup::command(&args, "unused")?;
    let bytes = std::fs::read(&backup)?;
    assert!(rfd_bot::backup::command(&args, "unused").is_err());
    assert_eq!(std::fs::read(&backup)?, bytes);
    Store::open(&backup)?.bind_application("1001")?;
    use std::os::unix::fs::PermissionsExt;
    assert_eq!(
        std::fs::metadata(&backup)?.permissions().mode() & 0o777,
        0o600
    );
    Ok(())
}
#[test]
fn duplicate_thread_aliases_survive_payload_upsert_and_old_trigger_upgrade() -> Result<()> {
    let dir = tempfile::tempdir()?;
    let path = dir.path().join("fixture.sqlite");
    let mut store = Store::open(&path)?;
    let mut d = deal("canonical", "alias");
    d.threads.push(ThreadContext {
        document_id: "alias".into(),
        post_url: "https://forums.redflagdeals.com/renamed-12345".into(),
        ..Default::default()
    });
    store.batch_write(&[d.clone()], &[])?;
    d.title = "Updated title".into();
    store.batch_write(&[], &[d.clone()])?;
    assert_eq!(store.by_ids(&["alias".into()])?["alias"], d);
    rusqlite::Connection::open(&path)?.execute_batch("DROP TRIGGER deals_threads_update; CREATE TRIGGER deals_threads_update AFTER UPDATE OF payload ON deals BEGIN DELETE FROM deal_threads WHERE deal_id=OLD.id; INSERT OR IGNORE INTO deal_threads SELECT json_extract(value,'$.DocumentID'),NEW.id FROM json_each(NEW.payload,'$.Threads'); END;")?;
    drop(store);
    let mut store = Store::open(&path)?;
    d.title = "After upgrade".into();
    store.batch_write(&[], &[d.clone()])?;
    assert_eq!(store.by_ids(&["alias".into()])?["alias"], d);
    store.integrity_check()?;
    Ok(())
}

#[test]
fn go_json_blobs_upgrade_without_changing_receipts_or_subscriptions() {
    let temporary = tempfile::tempdir().unwrap();
    let path = temporary.path().join("go-fixture.sqlite");
    let mut store = rfd_bot::storage::Store::open(&path).unwrap();
    store.bind_application("1001").unwrap();
    store
        .batch_write(
            &[rfd_bot::models::DealInfo {
                document_id: "fixture".into(),
                discord_message_ids: std::collections::BTreeMap::from([(
                    "42".into(),
                    "receipt".into(),
                )]),
                ..Default::default()
            }],
            &[],
        )
        .unwrap();
    store
        .save_subscription(&rfd_bot::models::Subscription {
            guild_id: "1".into(),
            channel_id: "42".into(),
            deal_type: "rfd_all".into(),
            subscription_type: "rfd".into(),
            added_at: rfd_bot::time::Timestamp::now(),
            ..Default::default()
        })
        .unwrap();
    drop(store);
    let legacy = rusqlite::Connection::open(&path).unwrap();
    legacy.execute_batch("UPDATE deals SET payload=CAST(payload AS BLOB); UPDATE subscriptions SET payload=CAST(payload AS BLOB); UPDATE settings SET payload=CAST(payload AS BLOB)").unwrap();
    drop(legacy);
    let mut upgraded = rfd_bot::storage::Store::open(&path).unwrap();
    upgraded.bind_application("1001").unwrap();
    assert_eq!(
        upgraded
            .deal("fixture")
            .unwrap()
            .unwrap()
            .discord_message_ids["42"],
        "receipt"
    );
    assert_eq!(upgraded.subscriptions(None).unwrap().len(), 1);
    drop(upgraded);
    let legacy = rusqlite::Connection::open(&path).unwrap();
    legacy.execute_batch("PRAGMA journal_mode=DELETE; UPDATE settings SET payload=x'ff' WHERE key='discord-application-id'").unwrap();
    assert!(rfd_bot::storage::Store::open(&path).is_err());
    let journal: String = legacy
        .query_row("PRAGMA journal_mode", [], |r| r.get(0))
        .unwrap();
    assert_eq!(journal, "delete");
    let kind: String = legacy
        .query_row(
            "SELECT typeof(payload) FROM settings WHERE key='discord-application-id'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(kind, "blob");
}
