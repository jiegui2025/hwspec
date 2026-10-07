package collect

import (
	"fmt"
	"time"
)

// slowAnswer bounds a read the kernel may answer slowly or never: hwmon
// and SPD reads go over SMBus, ethtool waits for the RTNL lock, a codec's
// proc file for the codec. Tests shorten it.
var slowAnswer = 2 * time.Second

// commandGrace is how long a program killed at its timeout gets to close
// its output, and then to exit, before it's left behind.
const commandGrace = 2 * time.Second

// noAnswer is a call given up on after its time.
type noAnswer time.Duration

func (d noAnswer) Error() string {
	return fmt.Sprintf("didn't answer within %v; left out", time.Duration(d))
}

// within runs f, or gives up on it after d. A read, or a program, stuck
// in the kernel (uninterruptible sleep) can't be cancelled or killed, so
// it's left behind: its goroutine ends when it does, or with the process,
// right after the capture.
func within[T any](d time.Duration, f func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := f()
		done <- result{v, err}
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case r := <-done:
		return r.v, r.err
	case <-t.C:
		var zero T
		return zero, noAnswer(d)
	}
}
