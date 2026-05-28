package cass_test

import (
	"fmt"

	"github.com/tmc/cc/cass"
)

func ExampleSearchRequest() {
	req := cass.SearchRequest{
		Query:   "auth bug",
		Mode:    cass.SearchLexical,
		Sort:    cass.SortRelevance,
		Filters: cass.Filters{Agent: "claude-code"},
		Limit:   10,
	}
	fmt.Println(req.Query, req.Sort, req.Filters.Agent, req.Limit)
	// Output: auth bug relevance claude-code 10
}
