//! Publish a consistent committed SQLite snapshot without overwriting any destination.
use anyhow::{Result, ensure};
use rusqlite::Connection;
use std::{
    fs::{File, OpenOptions},
    os::unix::fs::OpenOptionsExt,
    path::{Path, PathBuf},
};
struct Temporary(PathBuf);
impl Drop for Temporary {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.0);
    }
}
pub fn write(db: &Connection, destination: &Path) -> Result<()> {
    ensure!(!destination.exists(), "backup destination already exists");
    let parent = destination
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or_else(|| Path::new("."));
    let path = parent.join(format!(
        ".bot-snapshot-{}-{:016x}",
        std::process::id(),
        rand::random::<u64>()
    ));
    let file = OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(&path)?;
    drop(file);
    let temporary = Temporary(path);
    db.backup(rusqlite::MAIN_DB, &temporary.0, None)?;
    let check =
        Connection::open_with_flags(&temporary.0, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY)?;
    let integrity: String = check.query_row("PRAGMA integrity_check", [], |r| r.get(0))?;
    ensure!(integrity == "ok", "backup failed SQLite integrity check");
    drop(check);
    File::open(&temporary.0)?.sync_all()?;
    // A hard link publishes atomically and fails if another process created the destination.
    std::fs::hard_link(&temporary.0, destination)?;
    File::open(parent)?.sync_all()?;
    Ok(())
}
pub fn command(args: &[String], default: &str) -> Result<()> {
    #[derive(clap::Parser)]
    struct Backup {
        #[arg(long)]
        destination: PathBuf,
        #[arg(long)]
        database: Option<PathBuf>,
    }
    use clap::Parser;
    let opts =
        Backup::try_parse_from(std::iter::once("backup".to_owned()).chain(args.iter().cloned()))?;
    let database = opts
        .database
        .unwrap_or_else(|| crate::env::value("SQLITE_PATH", default).into());
    let store = crate::storage::Store::open(&database)?;
    write(&store.db, &opts.destination)?;
    println!("Consistent SQLite snapshot written.");
    Ok(())
}
