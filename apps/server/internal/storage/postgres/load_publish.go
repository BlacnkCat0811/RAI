package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"rai/server/internal/analysis"
	tl "rai/server/internal/trainingload"

	"github.com/jackc/pgx/v5"
)

func insertModelArtifact(ctx context.Context, tx pgx.Tx, dataset, run, key, schema string, value any, limit int) error {
	b, err := json.Marshal(value)
	if err != nil {
		return analysis.Invalid()
	}
	if len(b) > limit {
		return &analysis.Failure{Code: "analysis_input_limit"}
	}
	hash, _ := hex.DecodeString(tl.Hash(value))
	_, err = tx.Exec(ctx, `INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload) VALUES($1,$2,$3,$4,$5,$6)`, dataset, run, key, schema, hash, json.RawMessage(b))
	return err
}
