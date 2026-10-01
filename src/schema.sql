CREATE TABLE IF NOT EXISTS deals(id TEXT PRIMARY KEY,payload TEXT NOT NULL,published_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS deals_published ON deals(published_at);
 CREATE INDEX IF NOT EXISTS deals_updated ON deals(updated_at DESC,id);
 CREATE TABLE IF NOT EXISTS subscriptions(guild_id TEXT NOT NULL,channel_id TEXT NOT NULL,filter TEXT NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(guild_id,channel_id,filter));
 CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS deal_threads(thread_id TEXT NOT NULL,deal_id TEXT NOT NULL,PRIMARY KEY(thread_id,deal_id));
DROP TRIGGER IF EXISTS deals_threads_insert;
DROP TRIGGER IF EXISTS deals_threads_update;
CREATE TRIGGER IF NOT EXISTS deals_threads_insert AFTER INSERT ON deals BEGIN
		 INSERT OR IGNORE INTO deal_threads(thread_id,deal_id)
		 SELECT DISTINCT json_extract(thread.value,'$.DocumentID'),NEW.id
		 FROM json_each(CASE WHEN json_valid(NEW.payload) THEN NEW.payload ELSE '{}' END,'$.Threads') AS thread
		 WHERE json_extract(thread.value,'$.DocumentID') IS NOT NULL AND json_extract(thread.value,'$.DocumentID')<>'';
		 END;
CREATE TRIGGER IF NOT EXISTS deals_threads_update AFTER UPDATE OF payload ON deals WHEN OLD.payload<>NEW.payload BEGIN
		 DELETE FROM deal_threads WHERE deal_id=OLD.id;
		 INSERT OR IGNORE INTO deal_threads(thread_id,deal_id)
		 SELECT DISTINCT json_extract(thread.value,'$.DocumentID'),NEW.id
		 FROM json_each(CASE WHEN json_valid(NEW.payload) THEN NEW.payload ELSE '{}' END,'$.Threads') AS thread
		 WHERE json_extract(thread.value,'$.DocumentID') IS NOT NULL AND json_extract(thread.value,'$.DocumentID')<>'';
		 END;
CREATE TRIGGER IF NOT EXISTS deals_threads_delete AFTER DELETE ON deals BEGIN DELETE FROM deal_threads WHERE deal_id=OLD.id; END;
CREATE INDEX IF NOT EXISTS deal_threads_deal ON deal_threads(deal_id);
