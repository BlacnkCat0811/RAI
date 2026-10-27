package postgres

import (
	"context"

	"rai/server/internal/syncing"
)

func (s *Store) Changes(ctx context.Context, p syncing.Principal, after string, limit int) (syncing.ChangePage, error) {
	page := syncing.ChangePage{Changes: []syncing.Change{}, NextCursor: after}
	dataset, epoch, rev, ordinal, err := syncing.ParseChangeCursor(after)
	if err != nil {
		return page, err
	}
	if limit < 1 || limit > syncing.MaxChangePage {
		return page, syncing.Fail(422, "invalid_change_limit")
	}
	meta, err := s.Metadata(ctx, p)
	if err != nil {
		return page, err
	}
	if rev > 0 {
		if dataset != p.DatasetID || epoch != meta.DatasetEpoch {
			return page, syncing.Fail(422, "invalid_change_cursor")
		}
		var exists bool
		if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM change_events WHERE dataset_id=$1 AND commit_rev=$2 AND ordinal=$3)`, p.DatasetID, rev, ordinal).Scan(&exists); err != nil {
			return page, err
		}
		if !exists {
			return page, syncing.Fail(422, "invalid_change_cursor")
		}
	}
	rows, err := s.Pool.Query(ctx, `SELECT commit_rev,ordinal,event_id::text,entity_kind,entity_key,operation
		FROM change_events WHERE dataset_id=$1 AND (commit_rev,ordinal)>($2,$3)
		ORDER BY commit_rev,ordinal LIMIT $4`, p.DatasetID, rev, ordinal, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var change syncing.Change
		if err = rows.Scan(&change.Revision, &change.Ordinal, &change.EventID, &change.Kind, &change.Key, &change.Operation); err != nil {
			return page, err
		}
		if len(page.Changes) == limit {
			page.HasMore = true
			break
		}
		page.Changes = append(page.Changes, change)
		page.NextCursor = syncing.ChangeCursor(p.DatasetID, meta.DatasetEpoch, change.Revision, change.Ordinal)
	}
	return page, rows.Err()
}
