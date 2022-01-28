<<<<<<< HEAD
<<<<<<< HEAD
//go:build !windows
=======
>>>>>>> upgrade to k8s 1.23 lib
=======
//go:build !windows
>>>>>>> chore: Merge changes from upstream as of 2022-01-26 (#351)
// +build !windows

package cobra

var preExecHookFn func(*Command)
