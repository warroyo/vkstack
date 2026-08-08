package model

import "flag"

// update regenerates the committed docs/model.md instead of asserting against it.
var update = flag.Bool("update", false, "rewrite docs/model.md from the model")
