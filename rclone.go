package main

// Copied from example https://github.com/rclone/rclone_out_of_tree_example/tree/master

import (
	_ "github.com/elliotabbott/rclone-gopro-media-library/backend/gopro" // your out of tree backend
	_ "github.com/rclone/rclone/backend/all"                     // import all backends
	"github.com/rclone/rclone/cmd"
	_ "github.com/rclone/rclone/cmd/all"    // import all commands
	_ "github.com/rclone/rclone/lib/plugin" // import plugins
)

func main() {
	cmd.Main()
}
