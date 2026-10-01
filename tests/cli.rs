use std::process::Command;
#[test]
fn version_and_offline_storage_work_without_runtime_credentials() {
    let temp = tempfile::tempdir().unwrap();
    let version = Command::new(env!("CARGO_BIN_EXE_rfd-bot"))
        .arg("version")
        .env_clear()
        .current_dir(temp.path())
        .output()
        .unwrap();
    assert!(version.status.success());
    assert!(String::from_utf8(version.stdout).unwrap().contains("Rust"));
    let result = Command::new(env!("CARGO_BIN_EXE_rfd-bot"))
        .arg("check-storage")
        .env_clear()
        .env("SQLITE_PATH", temp.path().join("data/rfd.sqlite"))
        .current_dir(temp.path())
        .output()
        .unwrap();
    assert!(
        result.status.success(),
        "{}",
        String::from_utf8_lossy(&result.stderr)
    );
    assert!(temp.path().join("data/rfd.sqlite").exists());
    let config = Command::new(env!("CARGO_BIN_EXE_rfd-bot"))
        .arg("check-config")
        .env_clear()
        .current_dir(temp.path())
        .output()
        .unwrap();
    assert!(!config.status.success());
    assert!(!temp.path().join("data/rfd.sqlite-wal").exists());
}
