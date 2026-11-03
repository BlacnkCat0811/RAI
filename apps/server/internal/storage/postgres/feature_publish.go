package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	wf "rai/server/internal/workoutfeatures"
)

func featureReferences(n wf.RawWorkoutSnapshot) []wf.FactRef {
	refs := append([]wf.FactRef{n.Fact}, n.References...)
	for _, q := range n.Quantities {
		refs = append(refs, q.Fact)
	}
	for _, x := range n.Series {
		refs = append(refs, x.Fact)
	}
	for _, x := range n.Routes {
		refs = append(refs, x.Fact)
	}
	return refs
}
func writeFeatureArtifact(ctx context.Context, tx pgx.Tx, l Lease, id string, in analysis.Snapshot) error {
	f, n := in.FeatureArtifact, in.FeatureInput
	if f == nil || n == nil || f.Schema != wf.Schema || f.Algorithm != wf.Algorithm || f.Version != wf.Version || f.StackID != wf.StackID() || f.DatasetID != l.DatasetID || f.WorkoutID != n.WorkoutID || f.Generation != l.TargetGeneration || f.AsOf != n.AsOf || f.Evidence.InputHash != n.Hash() || f.Evidence.ConfigHash != n.Policy.Hash() {
		return analysis.Invalid()
	}
	b, h, err := f.MarshalArtifact()
	if err != nil {
		return featureFailure(err)
	}
	hash, _ := hex.DecodeString(h)
	var expected []byte
	if err = tx.QueryRow(ctx, `SELECT input_hash FROM analysis_runs WHERE dataset_id=$1 AND id=$2 AND job_id=$3 AND fencing_token=$4 AND status='RUNNING'`, l.DatasetID, id, l.ID, l.FencingToken).Scan(&expected); err != nil {
		return err
	}
	if hex.EncodeToString(expected) != f.Evidence.InputHash {
		return analysis.Invalid()
	}
	_, err = tx.Exec(ctx, `INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload) VALUES($1,$2,$3,$4,$5,$6)`, l.DatasetID, id, wf.ArtifactKey, wf.Schema, hash, json.RawMessage(b))
	return err
}
