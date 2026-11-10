//go:build integration

package integration

import (
	"errors"
	"testing"

	"rai/server/internal/admin"
	"rai/server/internal/syncing"
)

func newDatasetRequest(f *fixture) admin.NewDatasetRequest {
	return admin.NewDatasetRequest{OperationID: syncing.NewID(), SourceDataset: f.p.DatasetID, Actor: "synthetic-operator"}
}

func preparedCredential(t *testing.T) admin.Enrollment {
	t.Helper()
	c, e := admin.PrepareEnrollment()
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func requireSyncCode(t *testing.T, e error, code string) {
	t.Helper()
	var typed *syncing.Error
	if !errors.As(e, &typed) || typed.Code != code {
		t.Fatalf("expected %s; got %v", code, e)
	}
}
