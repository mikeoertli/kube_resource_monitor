package cli

import "k8s.io/apimachinery/pkg/runtime"

// runtimeObject is an alias kept local so the demo builder reads cleanly
// without importing apimachinery's runtime package everywhere.
type runtimeObject = runtime.Object

func toRuntimeObjects(in []runtimeObject) []runtime.Object {
	out := make([]runtime.Object, len(in))
	copy(out, in)
	return out
}
