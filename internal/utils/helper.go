package utils

import "os"

var isDemo = os.Getenv("IS_DEMO") == "true"

func InClause() string {
	inClause := ":anywhere"
	if isDemo {
		inClause = ":demoinbox"
	}
	return inClause
}

func OutputLabel() string {
	label := "jobs"
	if isDemo {
		label = "demooutput"
	}
	return label
}
