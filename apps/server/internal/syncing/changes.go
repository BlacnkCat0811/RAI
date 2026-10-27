package syncing

import (
	"strconv"
	"strings"
)

const MaxChangePage = 100

// Change is a bounded notification, not a complete fact or series download.
type Change struct {
	Revision  int64  `json:"revision"`
	Ordinal   int32  `json:"ordinal"`
	EventID   string `json:"eventID"`
	Kind      string `json:"kind"`
	Key       string `json:"key"`
	Operation string `json:"operation"`
}

type ChangePage struct {
	Changes    []Change `json:"changes"`
	NextCursor string   `json:"nextCursor"`
	HasMore    bool     `json:"hasMore"`
}

// ParseChangeCursor accepts the initial cursor or a canonical, epoch-bound
// position. The repository also verifies that the position actually exists.
func ParseChangeCursor(value string) (string, string, int64, int32, error) {
	if value == "0" {
		return "", "", 0, 0, nil
	}
	parts := strings.Split(value, ":")
	if len(parts) != 4 || !ValidID(parts[0]) || !ValidID(parts[1]) {
		return "", "", 0, 0, Fail(422, "invalid_change_cursor")
	}
	rev, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || rev < 1 || strconv.FormatInt(rev, 10) != parts[2] {
		return "", "", 0, 0, Fail(422, "invalid_change_cursor")
	}
	ord, err := strconv.ParseInt(parts[3], 10, 32)
	if err != nil || ord < 0 || strconv.FormatInt(ord, 10) != parts[3] {
		return "", "", 0, 0, Fail(422, "invalid_change_cursor")
	}
	return parts[0], parts[1], rev, int32(ord), nil
}

func ChangeCursor(dataset, epoch string, rev int64, ordinal int32) string {
	return dataset + ":" + epoch + ":" + strconv.FormatInt(rev, 10) + ":" + strconv.FormatInt(int64(ordinal), 10)
}
