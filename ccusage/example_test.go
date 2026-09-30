package ccusage_test

import (
	"fmt"
	"github.com/tmc/cc/ccusage"
	"time"
)

func ExampleRolling() {
	end := time.Unix(10000, 0)
	records := []ccusage.Record{{SessionID: "s", Time: end.Add(-time.Minute), Input: 10, Output: 2}}
	for _, span := range ccusage.Rolling(records, end, time.Hour) {
		fmt.Println(span.SessionID, span.Total())
	}
	// Output: s 12
}
