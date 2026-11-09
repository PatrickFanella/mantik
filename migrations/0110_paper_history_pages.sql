CREATE INDEX IF NOT EXISTS mantik_paper_history_page ON mantik_paper_record_epochs
    (kind,created_epoch,record_id COLLATE "C")
    WHERE kind IN ('decision','outcome');
COMMENT ON INDEX mantik_paper_history_page IS
    'Seek pages of sealed original identities by creation epoch and native byte ordering; payloads still require native replay';
