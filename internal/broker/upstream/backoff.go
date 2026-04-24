package upstream

import "time"

const (
	
	DefaultBaseDelay = time.Second
	DefaultMaxDelay = 30 * time.Second
)


type backoff struct {
	baseDelay      time.Duration
	maxDelay       time.Duration
	consecutiveFails int
}

func newBackoff(baseDelay, maxDelay time.Duration) *backoff {
	if baseDelay <= 0 {
		baseDelay = DefaultBaseDelay
	}
	if maxDelay <= 0 {
		maxDelay = DefaultMaxDelay
	}
	return &backoff{
		baseDelay: baseDelay,
		maxDelay:  maxDelay,
	}
}

func (b *backoff) failure() time.Duration {
	b.consecutiveFails++
	return b.currentDelay()
}


func (b *backoff) success(normalInterval time.Duration) time.Duration {
	b.consecutiveFails = 0
	return normalInterval
}


func (b *backoff) currentDelay() time.Duration {
	if b.consecutiveFails <= 0 {
		return b.baseDelay
	}
	delay := b.baseDelay
	for i := 1; i < b.consecutiveFails; i++ {
		delay *= 2
		if delay >= b.maxDelay {
			return b.maxDelay
		}
	}
	return delay
}
