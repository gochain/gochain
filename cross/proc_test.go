package cross

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gochain/gochain/v3/rpc"
)

func Test_cachedClientFn_get_timeout(t *testing.T) {
	cache := &cachedClientFn{
		fn: func() (*rpc.Client, error) { return nil, errors.New("fake error") },
	}
	ctx, _ := context.WithTimeout(context.Background(), time.Second)
	cl, err := cache.get(ctx)
	if cl != nil {
		t.Errorf("expected nil client but got: %v", cl)
	}
	if err != ctx.Err() {
		t.Errorf("expected ctx.Err() but got: %v", err)
	}
}

func Test_cachedClientSettable_get_timeout(t *testing.T) {
	cache := &cachedClientSettable{}
	ctx, _ := context.WithTimeout(context.Background(), time.Second)
	cl, err := cache.get(ctx)
	if cl != nil {
		t.Errorf("expected nil client but got: %v", cl)
	}
	if err != ctx.Err() {
		t.Errorf("expected ctx.Err() but got: %v", err)
	}
}
