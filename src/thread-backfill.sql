INSERT OR IGNORE INTO deal_threads(thread_id,deal_id)
		 SELECT json_extract(thread.value,'$.DocumentID'),deals.id
		 FROM deals,json_each(CASE WHEN json_valid(deals.payload) THEN deals.payload ELSE '{}' END,'$.Threads') AS thread
		 WHERE json_extract(thread.value,'$.DocumentID') IS NOT NULL AND json_extract(thread.value,'$.DocumentID')<>''