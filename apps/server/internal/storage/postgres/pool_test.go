package postgres

import (
	"context"
	"testing"
	"time"
)

func TestPoolConfiguration(t *testing.T) {
	base := "postgres://example@127.0.0.1:1/rai_dev?sslmode=disable"
	for _, tc := range []struct {
		url      string
		max      int32
		lifetime time.Duration
		idle     time.Duration
	}{
		{base, 8, 30 * time.Minute, 30 * time.Minute},
		{base + "&pool_max_conns=3&pool_min_conns=0&pool_max_conn_lifetime=12m&pool_max_conn_idle_time=4m", 3, 12 * time.Minute, 4 * time.Minute},
	} {
		s, err := Open(context.Background(), tc.url, 8)
		if err != nil {
			t.Fatal(err)
		}
		c := s.Pool.Config()
		if c.MaxConns != tc.max || c.MinConns != 0 || c.MaxConnLifetime != tc.lifetime || c.MaxConnIdleTime != tc.idle {
			t.Errorf("unexpected pool settings: max=%d min=%d lifetime=%s idle=%s", c.MaxConns, c.MinConns, c.MaxConnLifetime, c.MaxConnIdleTime)
		}
		s.Pool.Close()
	}
}
