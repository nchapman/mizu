-- Partial indexes for the two hot "where the row is in a small subset"
-- queries that the base indexes cannot serve:
--   * Unread timeline pages filter items by read_at IS NULL and order by
--     published_at DESC. items_published_idx covers ordering but not the
--     NULL predicate, so a heavy reader pays an O(items) filter scan.
--   * Webmention startup/receive paths look up rows by status='pending'
--     alone. mentions_target_idx leads on target, so it can't serve a
--     status-only scan.
-- Partial indexes are small (only the matching rows) and self-pruning as
-- items get marked read or mentions verify/reject.
CREATE INDEX items_unread_idx ON items(published_at DESC) WHERE read_at IS NULL;
CREATE INDEX mentions_pending_idx ON mentions(received_at) WHERE status = 'pending';
