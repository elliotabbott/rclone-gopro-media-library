package gopro_test

import (
	"testing"

	"github.com/elliotabbott/rclone-gopro-media-library/backend/gopro"
	"github.com/rclone/rclone/fstest/fstests"
)

// TestIntegration runs integration tests against the remote
func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName: "TestGoPro:",
		NilObject:  (*gopro.Object)(nil),
	})
}
