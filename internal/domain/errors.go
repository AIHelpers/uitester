package domain

import "fmt"

// errScenario builds a validation error without pulling in any external
// dependency, keeping the domain package dependency-free.
func errScenario(format string, args ...interface{}) error {
	return fmt.Errorf(format, args...)
}
