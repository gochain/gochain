package rawdb

import (
	"fmt"
	"time"

	"github.com/gochain/gochain/v5/common"
	"github.com/gochain/gochain/v5/log"
)

const (
	initialDelay = 1 * time.Second
	maxDelay     = 30 * time.Second
)

// Must executes fn, repeatedly retrying (with increasing delay) until it returns nil. op describes the operation for
// error logging.
func Must(op string, fn func() error) {
	err := fn()
	if err == nil {
		return
	}
	start := time.Now()
	cnt := 1
	delay := initialDelay
	msg := fmt.Sprintf("Failed to %q; retrying after delay", op)
	for err != nil {
		log.Error(msg, "attempt", cnt, "delay", common.PrettyDuration(delay), "err", err)
		cnt++
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
		time.Sleep(delay)
		err = fn()
	}
	log.Info(fmt.Sprintf("Successful %q after retrying", op), "attempts", cnt, "dur", common.PrettyDuration(time.Since(start)))
}
