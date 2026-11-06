package compose

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The network a running node is on is "byfn" when the CCaaS compose file
// was part of up, and "<project>_byfn" when it was not.
func TestIsNetworkFor(t *testing.T) {
	assert.True(t, isNetworkFor("byfn", "fnb"))
	assert.True(t, isNetworkFor("fnb_byfn", "fnb"))
	assert.False(t, isNetworkFor("other_byfn", "fnb"))
	assert.False(t, isNetworkFor("bridge", "fnb"))
}
