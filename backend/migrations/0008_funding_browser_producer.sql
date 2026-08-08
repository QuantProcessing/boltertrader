ALTER TABLE platform_work_items
    ADD COLUMN principal_id text NOT NULL DEFAULT '';

CREATE INDEX platform_work_items_browser_owner
    ON platform_work_items(principal_id, mode, lineage_id, created_at, work_item_id);
