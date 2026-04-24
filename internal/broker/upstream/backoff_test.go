package upstream

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewBackoff_Defaults(t *testing.T) {
	b := newBackoff(0, 0)
	assert.Equal(t, DefaultBaseDelay, b.baseDelay)
	assert.Equal(t, DefaultMaxDelay, b.maxDelay)
	assert.Equal(t, 0, b.consecutiveFails)
}

func TestNewBackoff_Custom(t *testing.T) {
	b := newBackoff(2*time.Second, time.Minute)
	assert.Equal(t, 2*time.Second, b.baseDelay)
	assert.Equal(t, time.Minute, b.maxDelay)
}

func TestBackoff_FailureIncreases(t *testing.T) {
	b := newBackoff(time.Second, 30*time.Second)

	// 1st failure: 1s
	d := b.failure()
	assert.Equal(t, time.Second, d)

	// 2nd failure: 2s
	d = b.failure()
	assert.Equal(t, 2*time.Second, d)

	// 3rd failure: 4s
	d = b.failure()
	assert.Equal(t, 4*time.Second, d)

	// 4th failure: 8s
	d = b.failure()
	assert.Equal(t, 8*time.Second, d)

	// 5th failure: 16s
	d = b.failure()
	assert.Equal(t, 16*time.Second, d)
}

func TestBackoff_MaxDelayCap(t *testing.T) {
	b := newBackoff(time.Second, 5*time.Second)

	b.failure() // 1s
	b.failure() // 2s
	b.failure() // 4s

	
	d := b.failure()
	assert.Equal(t, 5*time.Second, d)

	
	d = b.failure()
	assert.Equal(t, 5*time.Second, d)
}

func TestBackoff_SuccessResets(t *testing.T) {
	normalInterval := time.Minute
	b := newBackoff(time.Second, 30*time.Second)

	
	b.failure()
	b.failure()
	b.failure()
	assert.Equal(t, 3, b.consecutiveFails)


	d := b.success(normalInterval)
	assert.Equal(t, normalInterval, d)
	assert.Equal(t, 0, b.consecutiveFails)
}

func TestBackoff_ResetThenFailAgain(t *testing.T) {
	b := newBackoff(time.Second, 30*time.Second)

	// fail a few times
	b.failure()
	b.failure()
	b.failure()

	b.success(time.Minute)

	d := b.failure()
	assert.Equal(t, time.Second, d)

	d = b.failure()
	assert.Equal(t, 2*time.Second, d)
}

func TestBackoff_CurrentDelay_NoFailures(t *testing.T) {
	b := newBackoff(time.Second, 30*time.Second)
	d := b.currentDelay()
	assert.Equal(t, time.Second, d)
}
