package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
)

func (s *Store) Page(ctx context.Context, p syncing.Principal, id string) (syncing.PageState, error) {
	r, err := s.q.Page(ctx, dbgen.PageParams{DatasetID: p.DatasetID, PageID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.PageState{}, syncing.Fail(404, "page_not_found")
	}
	if err != nil {
		return syncing.PageState{}, err
	}
	state := "pending"
	if r.Receipt != nil {
		state = "sealed"
	}
	return syncing.PageState{PageID: r.PageID, StreamID: r.StreamID, DatasetEpoch: r.DatasetEpoch,
		CollectorEpoch: r.CollectorEpoch, QueryFingerprint: r.QueryFingerprint, FragmentCount: int(r.FragmentCount),
		FirstClientSeq: r.FirstClientSeq, CommittedFragments: int(r.CommittedFragments), IngestStatus: state,
		Receipt: json.RawMessage(r.Receipt)}, nil
}

// Called only under dataset -> stream locks, after immutable batch/seq checks.
// Budget: one page PK lookup, open/legacy-page index probes, at most one insert.
// Pages must occupy consecutive stream sequences; no facts are scanned.
func registerPage(ctx context.Context, tx pgx.Tx, p syncing.Principal, e syncing.Envelope) error {
	r, err := dbgen.New(tx).Page(ctx, dbgen.PageParams{DatasetID: p.DatasetID, PageID: e.PageID})
	if err == nil {
		if r.StreamID != e.StreamID || r.DatasetEpoch != e.DatasetEpoch || r.CollectorEpoch != e.CollectorEpoch ||
			r.QueryFingerprint != e.QueryFingerprint || int(r.FragmentCount) != e.FragmentCount ||
			r.FirstClientSeq != e.ClientSeq-int64(e.FragmentIndex) {
			return syncing.Fail(409, "page_identity_conflict")
		}
		if r.Receipt != nil {
			return syncing.Fail(409, "page_sealed")
		}
		if int(r.CommittedFragments) != e.FragmentIndex {
			return syncing.Fail(409, "page_fragment_gap")
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if e.FragmentIndex != 0 {
		return syncing.Fail(409, "page_fragment_gap")
	}
	// Reserve enough sequence space without overflowing bigint. fragmentCount is
	// validated before entry, so this subtraction is bounded and nonnegative.
	if e.ClientSeq > math.MaxInt64-int64(e.FragmentCount-1) {
		return syncing.Fail(422, "unsupported_payload")
	}
	legacy, err := dbgen.New(tx).LegacyPageExists(ctx, dbgen.LegacyPageExistsParams{DatasetID: p.DatasetID, PageID: e.PageID})
	if err != nil {
		return err
	}
	if legacy {
		return syncing.Fail(409, "page_identity_conflict")
	}
	open, err := dbgen.New(tx).OpenPageExists(ctx, dbgen.OpenPageExistsParams{DatasetID: p.DatasetID, StreamID: e.StreamID})
	if err != nil {
		return err
	}
	if open {
		return syncing.Fail(409, "page_incomplete")
	}
	_, err = tx.Exec(ctx, `INSERT INTO sync_pages(dataset_id,page_id,stream_id,dataset_epoch,collector_epoch,query_fingerprint,fragment_count,first_client_seq,manifest_sha256)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.DatasetID, e.PageID, e.StreamID, e.DatasetEpoch, e.CollectorEpoch, e.QueryFingerprint, e.FragmentCount, e.ClientSeq, make([]byte, sha256.Size))
	return err
}

// Each successful batch extends a fixed-size ordered manifest hash chain. The
// last fragment stores the immutable page receipt in the same transaction as
// facts, tombstones, ledger, jobs, batch receipt and stream position. The
// per-fragment query count is bounded regardless of page length (max 10,000).
// B-tree depth, lock waits and storage latency still depend on the environment.
func commitPage(ctx context.Context, tx pgx.Tx, p syncing.Principal, e syncing.Envelope, envelopeHash []byte, rev int64) error {
	r, err := dbgen.New(tx).Page(ctx, dbgen.PageParams{DatasetID: p.DatasetID, PageID: e.PageID})
	if err != nil {
		return err
	}
	if r.Receipt != nil || int(r.CommittedFragments) != e.FragmentIndex || r.FirstClientSeq != e.ClientSeq-int64(e.FragmentIndex) {
		return syncing.Fail(409, "page_fragment_gap")
	}
	h := sha256.New()
	_, _ = h.Write(r.ManifestSha256)
	_, _ = h.Write(envelopeHash)
	manifest := h.Sum(nil)
	var receipt []byte
	if e.FragmentIndex+1 == e.FragmentCount {
		receipt, err = json.Marshal(syncing.PageReceipt{PageID: e.PageID, StreamID: e.StreamID, DatasetEpoch: e.DatasetEpoch,
			CollectorEpoch: e.CollectorEpoch, QueryFingerprint: e.QueryFingerprint, IngestStatus: "sealed", FragmentCount: e.FragmentCount,
			FirstClientSeq: r.FirstClientSeq, LastClientSeq: e.ClientSeq, LastBatchID: e.BatchID,
			ManifestSHA256: hex.EncodeToString(manifest), CommitRevision: rev})
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE sync_pages SET committed_fragments=committed_fragments+1,manifest_sha256=$3,
 receipt=$4,sealed_at=CASE WHEN $4::bytea IS NULL THEN NULL ELSE now() END WHERE dataset_id=$1 AND page_id=$2`, p.DatasetID, e.PageID, manifest, receipt)
	return err
}
