package up

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateWaitFor(t *testing.T) {
	for _, phase := range []string{
		"", "onCreateCommand", "updateContentCommand", "postCreateCommand", "postStartCommand",
	} {
		cmd := &UpCmd{}
		cmd.WaitFor = phase
		require.NoError(t, cmd.validateWaitFor(), phase)
	}
	for _, phase := range []string{"postAttachCommand", "initializeCommand", "poststartcommand"} {
		cmd := &UpCmd{}
		cmd.WaitFor = phase
		require.ErrorContains(t, cmd.validateWaitFor(), "--wait-for must be one of", phase)
	}
}
